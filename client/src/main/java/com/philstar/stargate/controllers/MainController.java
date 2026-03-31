package com.philstar.stargate.controllers;

import com.philstar.stargate.AppState;
import com.philstar.stargate.GrpcClient;
import com.philstar.stargate.StarGateApp;
import com.philstar.stargate.proto.ActionResponse;
import com.philstar.stargate.proto.ChatSession;
import com.philstar.stargate.proto.Message;
import com.philstar.stargate.proto.MessageEvent;
import com.philstar.stargate.ui.SessionCell;
import io.grpc.StatusRuntimeException;
import io.grpc.stub.StreamObserver;
import java.awt.EventQueue;
import java.awt.SystemTray;
import java.awt.TrayIcon;
import java.awt.TrayIcon.MessageType;
import java.awt.image.BufferedImage;
import javafx.application.Platform;
import javafx.collections.transformation.FilteredList;
import javafx.concurrent.Task;
import javafx.fxml.FXML;
import javafx.geometry.Insets;
import javafx.geometry.Pos;
import javafx.scene.control.*;
import javafx.scene.layout.*;

import java.time.Instant;
import java.time.ZoneId;
import java.time.format.DateTimeFormatter;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Optional;

public class MainController {

    // AWT tray icon — initialized once, reused for all OS notifications.
    private TrayIcon trayIcon;
    private volatile String pendingSessionId;

    // Toolbar
    @FXML private Label  usernameLabel;
    @FXML private Button adminButton;
    @FXML private Button assignButton;
    @FXML private Button retireButton;

    // Session list
    @FXML private TextField             searchField;
    @FXML private ListView<ChatSession> sessionList;

    // Chat header
    @FXML private Label contactNameLabel;
    @FXML private Label contactGroupLabel;

    // Chat area
    @FXML private VBox       chatPanel;
    @FXML private Label      placeholderLabel;
    @FXML private VBox       messagesBox;
    @FXML private ScrollPane messagesScroll;

    // Reply bar
    @FXML private TextField replyField;

    // -------------------------------------------------------------------------

    @FXML
    public void initialize() {
        AppState state = AppState.get();
        usernameLabel.setText(state.getUsername());

        if (state.isHasGlobalAccess()) {
            adminButton.setVisible(true);
            adminButton.setManaged(true);
            assignButton.setVisible(true);
            assignButton.setManaged(true);
            retireButton.setVisible(true);
            retireButton.setManaged(true);
        }

        sessionList.setCellFactory(lv -> new SessionCell());

        FilteredList<ChatSession> filteredSessions = new FilteredList<>(state.getSessions());
        searchField.textProperty().addListener((obs, oldVal, newVal) -> {
            filteredSessions.setPredicate(session -> {
                if (newVal == null || newVal.isBlank()) return true;
                String[] terms = newVal.trim().toLowerCase().split("\\s+");
                String name  = state.getContactDisplayName(session.getContactPhone()).toLowerCase();
                String phone = session.getContactPhone().toLowerCase();
                String group = state.getGroupName(session.getGroupId()).toLowerCase();
                for (String term : terms) {
                    if (!name.contains(term) && !phone.contains(term) && !group.contains(term))
                        return false;
                }
                return true;
            });
        });
        sessionList.setItems(filteredSessions);
        sessionList.getSelectionModel().selectedItemProperty().addListener(
                (obs, old, selected) -> { if (selected != null) onSessionSelected(selected); });

        loadSessions();
        startSubscription();
        initTrayIcon();
    }

    // -------------------------------------------------------------------------
    // Session loading
    // -------------------------------------------------------------------------

    private void loadSessions() {
        AppState state = AppState.get();
        Task<List<ChatSession>> task = new Task<>() {
            @Override
            protected List<ChatSession> call() {
                return state.getGrpc().getSessions(state.getUserId());
            }
        };
        task.setOnSucceeded(e -> state.setSessions(task.getValue()));
        task.setOnFailed(e -> showError("Failed to load sessions: " + task.getException().getMessage()));
        bg(task);
    }

    private void onSessionSelected(ChatSession session) {
        AppState state = AppState.get();
        state.setSelectedSession(session);

        chatPanel.setVisible(true);
        chatPanel.setManaged(true);
        placeholderLabel.setVisible(false);
        placeholderLabel.setManaged(false);

        contactNameLabel.setText(state.getContactDisplayName(session.getContactPhone()));
        contactGroupLabel.setText(state.getGroupName(session.getGroupId()));

        Task<List<Message>> task = new Task<>() {
            @Override
            protected List<Message> call() {
                return state.getGrpc().getMessages(session.getSessionId());
            }
        };
        task.setOnSucceeded(e -> {
            renderMessages(task.getValue());
            scrollToBottom();
        });
        task.setOnFailed(e -> showError("Failed to load messages."));
        bg(task);
    }

    // -------------------------------------------------------------------------
    // Message rendering
    // -------------------------------------------------------------------------

    private void renderMessages(List<Message> messages) {
        messagesBox.getChildren().clear();
        for (Message msg : messages) {
            addBubble(msg.getText(), msg.getDirection(), msg.getTimestamp(),
                      msg.getSentByUserId(), msg.getSentByUsername());
        }
    }

    private void addBubble(String text, String direction, String timestamp,
                            String sentByUserId, String senderUsername) {
        boolean out  = "OUTBOUND".equals(direction);
        boolean mine = out && AppState.get().getUserId().equals(sentByUserId);

        Label textLabel = new Label(text);
        textLabel.setWrapText(true);
        textLabel.setMaxWidth(420);
        textLabel.setPadding(new Insets(8, 14, 8, 14));
        if (out) {
            textLabel.getStyleClass().add(mine ? "bubble-out-mine" : "bubble-out-other");
        } else {
            textLabel.getStyleClass().add("bubble-in");
        }

        String meta = formatTime(timestamp);
        if (out && !senderUsername.isEmpty()) {
            meta = senderUsername + " • " + meta;
        }
        Label metaLabel = new Label(meta);
        metaLabel.getStyleClass().add("bubble-time");
        if (out) metaLabel.setAlignment(Pos.CENTER_RIGHT);

        VBox bubble = new VBox(2, textLabel, metaLabel);

        HBox row = new HBox(bubble);
        row.setPadding(new Insets(3, 12, 3, 12));
        row.setAlignment(out ? Pos.CENTER_RIGHT : Pos.CENTER_LEFT);

        messagesBox.getChildren().add(row);
    }

    private static final DateTimeFormatter TIMESTAMP_FMT =
            DateTimeFormatter.ofPattern("yyyy-MM-dd HH:mm:ss").withZone(ZoneId.systemDefault());

    private String formatTime(String iso) {
        if (iso == null || iso.isEmpty()) return "";
        try {
            return TIMESTAMP_FMT.format(Instant.parse(iso));
        } catch (Exception e) {
            return "";
        }
    }

    private void scrollToBottom() {
        // Two deferred pulses: first lets JavaFX process the new nodes,
        // second fires after layout so the ScrollPane knows its full height.
        Platform.runLater(() -> Platform.runLater(() -> messagesScroll.setVvalue(1.0)));
    }

    // -------------------------------------------------------------------------
    // Real-time subscription
    // -------------------------------------------------------------------------

    private void startSubscription() {
        AppState state = AppState.get();
        state.getGrpc().subscribeToInbox(state.getUserId(), new StreamObserver<>() {
            @Override
            public void onNext(MessageEvent event) {
                Platform.runLater(() -> handleEvent(event));
            }
            @Override
            public void onError(Throwable t) {
                // Channel shut down on logout — nothing to do.
            }
            @Override
            public void onCompleted() {}
        });
    }

    private void handleEvent(MessageEvent event) {
        AppState state = AppState.get();

        // "system" events (from AssignContact) carry no message text; skip display.
        if (!event.getMessageText().isEmpty()) {
            ChatSession selected = state.getSelectedSession();
            if (selected != null && selected.getSessionId().equals(event.getSessionId())) {
                boolean inbound = "Contact".equals(event.getSenderType());
                String dir = inbound ? "INBOUND" : "OUTBOUND";
                addBubble(event.getMessageText(), dir, event.getTimestamp(),
                          inbound ? "" : event.getSenderType(),
                          inbound ? "" : event.getSenderUsername());
                scrollToBottom();
            }

            // Notify for inbound messages and messages from other users (not my own sends).
            boolean isMine = state.getUserId().equals(event.getSenderType());
            if (!isMine) {
                String phone = state.getSessions().stream()
                        .filter(s -> s.getSessionId().equals(event.getSessionId()))
                        .map(s -> s.getContactPhone())
                        .findFirst().orElse(null);
                String title = phone != null ? state.getContactDisplayName(phone) : "StarGate";
                boolean inbound = "Contact".equals(event.getSenderType());
                String body = inbound
                        ? event.getMessageText()
                        : event.getSenderUsername() + ": " + event.getMessageText();
                showNotification(title, body, event.getSessionId());
            }
        }

        // If this session isn't in our list yet, fetch and add it; otherwise promote it to top.
        boolean known = state.getSessions().stream()
                .anyMatch(s -> s.getSessionId().equals(event.getSessionId()));
        if (!known) {
            GrpcClient grpc = state.getGrpc();
            if (grpc == null) return;
            Task<ChatSession> task = new Task<>() {
                @Override
                protected ChatSession call() {
                    return grpc.getSession(event.getSessionId());
                }
            };
            task.setOnSucceeded(e -> state.replaceOrAddSession(task.getValue()));
            bg(task);
        } else {
            state.getSessions().stream()
                    .filter(s -> s.getSessionId().equals(event.getSessionId()))
                    .findFirst()
                    .ifPresent(state::replaceOrAddSession);
        }
    }

    // -------------------------------------------------------------------------
    // Reply
    // -------------------------------------------------------------------------

    @FXML
    private void onSend() {
        String text = replyField.getText().trim();
        if (text.isEmpty()) return;

        AppState state = AppState.get();
        ChatSession session = state.getSelectedSession();
        if (session == null) return;

        String userId    = state.getUserId();
        String username  = state.getUsername();
        String sessionId = session.getSessionId();

        replyField.setDisable(true);
        Task<ActionResponse> task = new Task<>() {
            @Override
            protected ActionResponse call() {
                return state.getGrpc().sendReply(sessionId, text, userId);
            }
        };
        task.setOnSucceeded(e -> {
            replyField.setDisable(false);
            ActionResponse resp = task.getValue();
            if (resp.getSuccess()) {
                replyField.clear();
                addBubble(text, "OUTBOUND", Instant.now().toString(), userId, username);
                scrollToBottom();
            } else {
                showError("Send failed: " + resp.getErrorMessage());
            }
        });
        task.setOnFailed(e -> {
            replyField.setDisable(false);
            showError("Failed to send: " + task.getException().getMessage());
        });
        bg(task);
    }

    // -------------------------------------------------------------------------
    // Contact actions
    // -------------------------------------------------------------------------

    @FXML
    private void onNewSession() {
        AppState state = AppState.get();
        Map<String, String> groups = state.getGroupNames();
        if (groups.isEmpty()) {
            showError("No groups available.");
            return;
        }

        List<String> groupNames = new ArrayList<>(groups.values());
        List<String> groupIds   = new ArrayList<>(groups.keySet());

        // Build a custom dialog: phone (required), group (required), name (optional)
        Dialog<ButtonType> dlg = new Dialog<>();
        dlg.setTitle("New Conversation");
        dlg.setHeaderText("Start an outbound conversation");
        dlg.getDialogPane().getButtonTypes().addAll(ButtonType.OK, ButtonType.CANCEL);

        GridPane grid = new GridPane();
        grid.setHgap(10);
        grid.setVgap(10);
        grid.setPadding(new Insets(16));

        TextField phoneField = new TextField();
        phoneField.setPromptText("+63...");

        ChoiceBox<String> groupChoice = new ChoiceBox<>();
        groupChoice.getItems().addAll(groupNames);
        groupChoice.getSelectionModel().selectFirst();
        groupChoice.setMaxWidth(Double.MAX_VALUE);

        TextField nameField = new TextField();
        nameField.setPromptText("Optional");

        grid.add(new Label("Phone number:"), 0, 0);
        grid.add(phoneField, 1, 0);
        grid.add(new Label("Group:"), 0, 1);
        grid.add(groupChoice, 1, 1);
        grid.add(new Label("Contact name:"), 0, 2);
        grid.add(nameField, 1, 2);
        GridPane.setHgrow(phoneField,   javafx.scene.layout.Priority.ALWAYS);
        GridPane.setHgrow(groupChoice,  javafx.scene.layout.Priority.ALWAYS);
        GridPane.setHgrow(nameField,    javafx.scene.layout.Priority.ALWAYS);

        dlg.getDialogPane().setContent(grid);
        dlg.getDialogPane().lookupButton(ButtonType.OK).setDisable(true);
        phoneField.textProperty().addListener((obs, o, n) ->
                dlg.getDialogPane().lookupButton(ButtonType.OK).setDisable(n.trim().isEmpty()));
        Platform.runLater(phoneField::requestFocus);

        Optional<ButtonType> result = dlg.showAndWait();
        if (result.isEmpty() || result.get() != ButtonType.OK) return;

        String raw     = phoneField.getText().trim();
        String phone   = raw.startsWith("0") ? "+63" + raw.substring(1) : raw;
        String groupId = groupIds.get(groupChoice.getSelectionModel().getSelectedIndex());
        String name    = nameField.getText().trim();

        Task<ChatSession> task = new Task<>() {
            @Override
            protected ChatSession call() {
                return state.getGrpc().createSession(phone, groupId, name, state.getUserId());
            }
        };
        task.setOnSucceeded(e -> {
            ChatSession sess = task.getValue();
            if (!name.isEmpty()) state.setContactName(phone, name);
            state.replaceOrAddSession(sess);
            state.setSelectedSession(sess);
            sessionList.getSelectionModel().select(sess);
        });
        task.setOnFailed(e -> {
            Throwable ex = task.getException();
            String msg = (ex instanceof StatusRuntimeException sre)
                    ? sre.getStatus().getDescription()
                    : "Failed to create session.";
            showError(msg);
        });
        bg(task);
    }

    @FXML
    private void onRename() {
        AppState state = AppState.get();
        ChatSession session = state.getSelectedSession();
        if (session == null) return;

        String phone   = session.getContactPhone();
        String current = state.getContactDisplayName(phone);
        String hint    = current.equals(phone) ? "" : current;

        TextInputDialog dlg = new TextInputDialog(hint);
        dlg.setTitle("Rename Contact");
        dlg.setHeaderText(phone);
        dlg.setContentText("Name:");
        Optional<String> result = dlg.showAndWait();

        result.filter(s -> !s.isBlank()).ifPresent(name -> {
            Task<ActionResponse> task = new Task<>() {
                @Override
                protected ActionResponse call() {
                    return state.getGrpc().renameContact(phone, name, state.getUserId());
                }
            };
            task.setOnSucceeded(e -> {
                state.setContactName(phone, name);
                contactNameLabel.setText(name);
                sessionList.refresh();
            });
            task.setOnFailed(e -> showError("Rename failed."));
            bg(task);
        });
    }

    @FXML
    private void onAssign() {
        AppState state = AppState.get();
        ChatSession session = state.getSelectedSession();
        if (session == null) return;

        Map<String, String> groups = state.getGroupNames();
        if (groups.isEmpty()) {
            showError("No groups available. Check the server.");
            return;
        }

        List<String> names = new ArrayList<>(groups.values());
        List<String> ids   = new ArrayList<>(groups.keySet());
        String currentGroup = state.getGroupName(session.getGroupId());
        String defaultChoice = names.contains(currentGroup) ? currentGroup : names.get(0);

        ChoiceDialog<String> dlg = new ChoiceDialog<>(defaultChoice, names);
        dlg.setTitle("Assign Contact");
        dlg.setHeaderText(state.getContactDisplayName(session.getContactPhone()));
        dlg.setContentText("Group:");
        Optional<String> result = dlg.showAndWait();

        result.ifPresent(chosenName -> {
            String chosenId = ids.get(names.indexOf(chosenName));
            Task<ActionResponse> task = new Task<>() {
                @Override
                protected ActionResponse call() {
                    return state.getGrpc().assignContact(
                            session.getContactPhone(), chosenId, state.getUserId());
                }
            };
            task.setOnSucceeded(e -> {
                contactGroupLabel.setText(chosenName);
                // Rebuild the session with the new groupId so SessionCell re-renders correctly.
                ChatSession updated = session.toBuilder().setGroupId(chosenId).build();
                state.replaceOrAddSession(updated);
                state.setSelectedSession(updated);
            });
            task.setOnFailed(e -> showError("Assign failed."));
            bg(task);
        });
    }

    @FXML
    private void onRetire() {
        AppState state = AppState.get();
        ChatSession session = state.getSelectedSession();
        if (session == null) return;

        String display = state.getContactDisplayName(session.getContactPhone());
        Alert confirm = new Alert(Alert.AlertType.CONFIRMATION,
                "Close the current conversation for " + display + " and create a fresh unknown contact?",
                ButtonType.OK, ButtonType.CANCEL);
        confirm.setTitle("Retire Contact");
        confirm.setHeaderText(null);

        confirm.showAndWait().filter(b -> b == ButtonType.OK).ifPresent(b -> {
            String phone  = session.getContactPhone();
            String userId = state.getUserId();

            Task<ChatSession> task = new Task<>() {
                @Override
                protected ChatSession call() {
                    return state.getGrpc().retireContact(phone, userId);
                }
            };
            task.setOnSucceeded(e -> {
                ChatSession fresh = task.getValue();
                state.replaceOrAddSession(fresh);
                state.setSelectedSession(fresh);
                sessionList.getSelectionModel().select(fresh);
                contactNameLabel.setText(fresh.getContactPhone());
                contactGroupLabel.setText("Unassigned");
                messagesBox.getChildren().clear();
            });
            task.setOnFailed(e -> showError("Retire failed."));
            bg(task);
        });
    }

    // -------------------------------------------------------------------------
    // Logout
    // -------------------------------------------------------------------------

    @FXML
    private void onAdmin() {
        try {
            StarGateApp.showAdmin();
        } catch (Exception e) {
            showError("Failed to open admin panel.");
        }
    }

    @FXML
    private void onChangePassword() {
        Dialog<ButtonType> dlg = new Dialog<>();
        dlg.setTitle("Change Password");
        dlg.setHeaderText(null);
        dlg.getDialogPane().getButtonTypes().addAll(ButtonType.OK, ButtonType.CANCEL);

        GridPane grid = new GridPane();
        grid.setHgap(10);
        grid.setVgap(10);
        grid.setPadding(new Insets(16));

        PasswordField currentField = new PasswordField();
        PasswordField newField     = new PasswordField();
        PasswordField confirmField = new PasswordField();

        grid.add(new Label("Current password:"), 0, 0); grid.add(currentField, 1, 0);
        grid.add(new Label("New password:"),     0, 1); grid.add(newField,     1, 1);
        grid.add(new Label("Confirm password:"), 0, 2); grid.add(confirmField, 1, 2);
        GridPane.setHgrow(currentField, javafx.scene.layout.Priority.ALWAYS);
        GridPane.setHgrow(newField,     javafx.scene.layout.Priority.ALWAYS);
        GridPane.setHgrow(confirmField, javafx.scene.layout.Priority.ALWAYS);

        dlg.getDialogPane().setContent(grid);
        javafx.scene.Node okBtn = dlg.getDialogPane().lookupButton(ButtonType.OK);
        okBtn.setDisable(true);

        Runnable validate = () -> okBtn.setDisable(
                currentField.getText().isEmpty() ||
                newField.getText().isEmpty() ||
                !newField.getText().equals(confirmField.getText()));
        currentField.textProperty().addListener((o, a, b) -> validate.run());
        newField.textProperty().addListener((o, a, b) -> validate.run());
        confirmField.textProperty().addListener((o, a, b) -> validate.run());

        Platform.runLater(currentField::requestFocus);
        if (dlg.showAndWait().orElse(ButtonType.CANCEL) != ButtonType.OK) return;

        String current = currentField.getText();
        String newPass  = newField.getText();
        AppState state  = AppState.get();

        Task<ActionResponse> task = new Task<>() {
            @Override protected ActionResponse call() {
                return state.getGrpc().changePassword(state.getUserId(), current, newPass);
            }
        };
        task.setOnSucceeded(e -> {
            ActionResponse resp = task.getValue();
            if (resp.getSuccess()) {
                new Alert(Alert.AlertType.INFORMATION, "Password changed successfully.", ButtonType.OK).show();
            } else {
                showError(resp.getErrorMessage());
            }
        });
        task.setOnFailed(e -> showError("Failed to change password."));
        bg(task);
    }

    @FXML
    private void onLogout() {
        removeTrayIcon();
        AppState.get().reset();
        try {
            StarGateApp.showLogin();
        } catch (Exception e) {
            showError("Failed to return to login screen.");
        }
    }

    // -------------------------------------------------------------------------
    // Helpers
    // -------------------------------------------------------------------------

    private void initTrayIcon() {
        if (!SystemTray.isSupported()) return;
        EventQueue.invokeLater(() -> {
            try {
                BufferedImage img = new BufferedImage(16, 16, BufferedImage.TYPE_INT_RGB);
                var g = img.createGraphics();
                g.setColor(new java.awt.Color(0x0f, 0x34, 0x60));
                g.fillRect(0, 0, 16, 16);
                g.dispose();

                trayIcon = new TrayIcon(img, "StarGate");
                trayIcon.setImageAutoSize(true);
                trayIcon.addActionListener(e -> navigateToPendingSession());
                SystemTray.getSystemTray().add(trayIcon);
            } catch (Exception ignored) {}
        });
    }

    private void showNotification(String title, String body, String sessionId) {
        if (trayIcon == null) return;
        pendingSessionId = sessionId;
        EventQueue.invokeLater(() -> trayIcon.displayMessage(title, body, MessageType.INFO));
    }

    private void navigateToPendingSession() {
        String sid = pendingSessionId;
        if (sid == null) return;
        Platform.runLater(() -> {
            javafx.stage.Stage stage = StarGateApp.getPrimaryStage();
            stage.setIconified(false);
            stage.toFront();
            stage.requestFocus();
            AppState.get().getSessions().stream()
                    .filter(s -> s.getSessionId().equals(sid))
                    .findFirst()
                    .ifPresent(s -> sessionList.getSelectionModel().select(s));
        });
    }

    private void removeTrayIcon() {
        if (trayIcon == null) return;
        EventQueue.invokeLater(() -> {
            SystemTray.getSystemTray().remove(trayIcon);
            trayIcon = null;
        });
    }

    private void bg(Task<?> task) {
        Thread t = new Thread(task);
        t.setDaemon(true);
        t.start();
    }

    private void showError(String msg) {
        Platform.runLater(() -> new Alert(Alert.AlertType.ERROR, msg, ButtonType.OK).show());
    }
}
