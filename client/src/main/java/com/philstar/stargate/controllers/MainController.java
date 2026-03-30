package com.philstar.stargate.controllers;

import com.philstar.stargate.AppState;
import com.philstar.stargate.GrpcClient;
import com.philstar.stargate.StarGateApp;
import com.philstar.stargate.proto.ActionResponse;
import com.philstar.stargate.proto.ChatSession;
import com.philstar.stargate.proto.Message;
import com.philstar.stargate.proto.MessageEvent;
import com.philstar.stargate.ui.SessionCell;
import io.grpc.stub.StreamObserver;
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

    // Toolbar
    @FXML private Label usernameLabel;
    @FXML private Button adminButton;

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
        Platform.runLater(() -> messagesScroll.setVvalue(1.0));
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
        }

        // If this session isn't in our list yet, fetch and add it.
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

        replyField.clear();
        addBubble(text, "OUTBOUND", Instant.now().toString(),
                  state.getUserId(), state.getUsername());
        scrollToBottom();

        String userId = state.getUserId();
        String sessionId = session.getSessionId();

        Task<ActionResponse> task = new Task<>() {
            @Override
            protected ActionResponse call() {
                return state.getGrpc().sendReply(sessionId, text, userId);
            }
        };
        task.setOnFailed(e -> showError("Failed to send: " + task.getException().getMessage()));
        bg(task);
    }

    // -------------------------------------------------------------------------
    // Contact actions
    // -------------------------------------------------------------------------

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
    private void onLogout() {
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

    private void bg(Task<?> task) {
        Thread t = new Thread(task);
        t.setDaemon(true);
        t.start();
    }

    private void showError(String msg) {
        Platform.runLater(() -> new Alert(Alert.AlertType.ERROR, msg, ButtonType.OK).show());
    }
}
