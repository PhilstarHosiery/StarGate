package com.philstar.stargate.controllers;

import com.philstar.stargate.AppState;
import com.philstar.stargate.proto.ActionResponse;
import com.philstar.stargate.proto.Group;
import com.philstar.stargate.proto.UserInfo;
import javafx.application.Platform;
import javafx.beans.property.BooleanProperty;
import javafx.beans.property.SimpleStringProperty;
import javafx.beans.property.SimpleBooleanProperty;
import javafx.collections.FXCollections;
import javafx.collections.ObservableList;
import javafx.concurrent.Task;
import javafx.fxml.FXML;
import javafx.scene.control.*;
import javafx.scene.control.cell.CheckBoxListCell;
import javafx.util.StringConverter;

import java.util.HashMap;
import java.util.List;
import java.util.Map;

public class AdminController {

    // Groups tab
    @FXML private TableView<Group>              groupsTable;
    @FXML private TableColumn<Group, String>    groupNameCol;
    @FXML private TextField                     groupNameField;

    // Users tab
    @FXML private TableView<UserInfo>           usersTable;
    @FXML private TableColumn<UserInfo, String> userNameCol;
    @FXML private TableColumn<UserInfo, String> userGlobalCol;
    @FXML private TextField                     newUsernameField;
    @FXML private PasswordField                 newPasswordField;
    @FXML private CheckBox                      newUserGlobalCheck;

    // Permissions tab
    @FXML private Tab                           permissionsTab;
    @FXML private ListView<UserInfo>            permUsersList;
    @FXML private CheckBox                      permGlobalCheck;
    @FXML private ListView<Group>               permGroupsList;

    private final ObservableList<Group>    groups = FXCollections.observableArrayList();
    private final ObservableList<UserInfo> users  = FXCollections.observableArrayList();

    /** Per-group checkbox state for the Permissions tab. */
    private final Map<String, BooleanProperty> groupChecked = new HashMap<>();

    @FXML
    public void initialize() {
        // Groups table
        groupNameCol.setCellValueFactory(c -> new SimpleStringProperty(c.getValue().getName()));
        groupsTable.setItems(groups);

        // Users table
        userNameCol.setCellValueFactory(c -> new SimpleStringProperty(c.getValue().getUsername()));
        userGlobalCol.setCellValueFactory(c ->
                new SimpleStringProperty(c.getValue().getHasGlobalAccess() ? "Yes" : "No"));
        usersTable.setItems(users);

        // Permissions — users list
        permUsersList.setItems(users);
        permUsersList.setCellFactory(lv -> new ListCell<>() {
            @Override protected void updateItem(UserInfo u, boolean empty) {
                super.updateItem(u, empty);
                setText(empty || u == null ? null : u.getUsername());
            }
        });
        permUsersList.getSelectionModel().selectedItemProperty().addListener(
                (obs, old, sel) -> { if (sel != null) onPermUserSelected(sel); });

        // Permissions — groups list with checkboxes
        permGroupsList.setItems(groups);
        permGroupsList.setCellFactory(CheckBoxListCell.forListView(
                group -> groupChecked.computeIfAbsent(group.getId(), id -> new SimpleBooleanProperty(false)),
                new StringConverter<>() {
                    @Override public String toString(Group g)   { return g == null ? "" : g.getName(); }
                    @Override public Group  fromString(String s){ return null; }
                }));

        // JavaFX ListView virtual flow doesn't initialize properly in a hidden Tab.
        // Refresh both lists whenever the Permissions tab is selected so cells render.
        permissionsTab.selectedProperty().addListener((obs, wasSelected, isSelected) -> {
            if (isSelected) {
                permUsersList.refresh();
                permGroupsList.refresh();
            }
        });

        loadData();
    }

    private void loadData() {
        AppState state = AppState.get();
        Task<Void> task = new Task<>() {
            @Override protected Void call() {
                List<Group>    g = state.getGrpc().listGroups(state.getUserId());
                List<UserInfo> u = state.getGrpc().listUsers(state.getUserId());
                Platform.runLater(() -> {
                    groups.setAll(g);
                    users.setAll(u);
                    // Pre-populate checked map entries for all groups
                    for (Group gr : g) {
                        groupChecked.computeIfAbsent(gr.getId(), id -> new SimpleBooleanProperty(false));
                    }
                });
                return null;
            }
        };
        task.setOnFailed(e -> showError("Failed to load data: " + task.getException().getMessage()));
        bg(task);
    }

    private void onPermUserSelected(UserInfo user) {
        // Reset all groups to unchecked, then check the user's groups.
        groupChecked.values().forEach(p -> p.set(false));
        for (String gid : user.getGroupIdsList()) {
            BooleanProperty p = groupChecked.get(gid);
            if (p != null) p.set(true);
        }
        permGlobalCheck.setSelected(user.getHasGlobalAccess());
    }

    // -------------------------------------------------------------------------
    // Groups
    // -------------------------------------------------------------------------

    @FXML
    private void onAddGroup() {
        String name = groupNameField.getText().trim();
        if (name.isBlank()) return;

        AppState state = AppState.get();
        Task<Group> task = new Task<>() {
            @Override protected Group call() {
                return state.getGrpc().createGroup(name, state.getUserId());
            }
        };
        task.setOnSucceeded(e -> {
            Group g = task.getValue();
            groups.add(g);
            groupChecked.put(g.getId(), new SimpleBooleanProperty(false));
            state.loadGroups(List.copyOf(groups));
            groupNameField.clear();
        });
        task.setOnFailed(e -> showError("Failed to add group: " + task.getException().getMessage()));
        bg(task);
    }

    @FXML
    private void onRenameGroup() {
        Group selected = groupsTable.getSelectionModel().getSelectedItem();
        if (selected == null) { showError("Select a group first."); return; }

        TextInputDialog dlg = new TextInputDialog(selected.getName());
        dlg.setTitle("Rename Group");
        dlg.setHeaderText(null);
        dlg.setContentText("New name:");
        dlg.showAndWait().filter(s -> !s.isBlank() && !s.equals(selected.getName())).ifPresent(newName -> {
            AppState state = AppState.get();
            Task<ActionResponse> task = new Task<>() {
                @Override protected ActionResponse call() {
                    return state.getGrpc().renameGroup(selected.getId(), newName, state.getUserId());
                }
            };
            task.setOnSucceeded(e -> {
                if (task.getValue().getSuccess()) {
                    state.loadGroups(List.copyOf(groups));
                    loadData();
                } else {
                    showError(task.getValue().getErrorMessage());
                }
            });
            task.setOnFailed(e -> showError("Rename failed."));
            bg(task);
        });
    }

    @FXML
    private void onDeleteGroup() {
        Group selected = groupsTable.getSelectionModel().getSelectedItem();
        if (selected == null) { showError("Select a group first."); return; }

        Alert confirm = new Alert(Alert.AlertType.CONFIRMATION,
                "Delete group \"" + selected.getName() + "\"?",
                ButtonType.OK, ButtonType.CANCEL);
        confirm.setTitle("Delete Group");
        confirm.setHeaderText(null);
        confirm.showAndWait().filter(b -> b == ButtonType.OK).ifPresent(b -> {
            AppState state = AppState.get();
            Task<ActionResponse> task = new Task<>() {
                @Override protected ActionResponse call() {
                    return state.getGrpc().deleteGroup(selected.getId(), state.getUserId());
                }
            };
            task.setOnSucceeded(e -> {
                ActionResponse resp = task.getValue();
                if (resp.getSuccess()) {
                    groups.remove(selected);
                    groupChecked.remove(selected.getId());
                    state.loadGroups(List.copyOf(groups));
                } else {
                    showError(resp.getErrorMessage());
                }
            });
            task.setOnFailed(e -> showError("Delete failed."));
            bg(task);
        });
    }

    // -------------------------------------------------------------------------
    // Users
    // -------------------------------------------------------------------------

    @FXML
    private void onAddUser() {
        String username = newUsernameField.getText().trim();
        String password = newPasswordField.getText();
        boolean globalAccess = newUserGlobalCheck.isSelected();

        if (username.isBlank() || password.isBlank()) {
            showError("Username and password are required.");
            return;
        }

        AppState state = AppState.get();
        Task<ActionResponse> task = new Task<>() {
            @Override protected ActionResponse call() {
                return state.getGrpc().createUser(username, password, globalAccess, state.getUserId());
            }
        };
        task.setOnSucceeded(e -> {
            ActionResponse resp = task.getValue();
            if (resp.getSuccess()) {
                newUsernameField.clear();
                newPasswordField.clear();
                newUserGlobalCheck.setSelected(false);
                loadData();
            } else {
                showError(resp.getErrorMessage());
            }
        });
        task.setOnFailed(e -> showError("Failed to create user: " + task.getException().getMessage()));
        bg(task);
    }

    @FXML
    private void onDeleteUser() {
        UserInfo selected = usersTable.getSelectionModel().getSelectedItem();
        if (selected == null) { showError("Select a user first."); return; }

        Alert confirm = new Alert(Alert.AlertType.CONFIRMATION,
                "Delete user \"" + selected.getUsername() + "\"? This cannot be undone.",
                ButtonType.OK, ButtonType.CANCEL);
        confirm.setTitle("Delete User");
        confirm.setHeaderText(null);
        confirm.showAndWait().filter(b -> b == ButtonType.OK).ifPresent(b -> {
            AppState state = AppState.get();
            Task<ActionResponse> task = new Task<>() {
                @Override protected ActionResponse call() {
                    return state.getGrpc().deleteUser(selected.getUserId(), state.getUserId());
                }
            };
            task.setOnSucceeded(e -> {
                ActionResponse resp = task.getValue();
                if (resp.getSuccess()) {
                    users.remove(selected);
                } else {
                    showError(resp.getErrorMessage());
                }
            });
            task.setOnFailed(e -> showError("Delete failed."));
            bg(task);
        });
    }

    // -------------------------------------------------------------------------
    // Permissions
    // -------------------------------------------------------------------------

    @FXML
    private void onSavePermissions() {
        UserInfo selected = permUsersList.getSelectionModel().getSelectedItem();
        if (selected == null) { showError("Select a user first."); return; }

        boolean globalAccess = permGlobalCheck.isSelected();
        List<String> selectedGroupIds = groups.stream()
                .filter(g -> {
                    BooleanProperty p = groupChecked.get(g.getId());
                    return p != null && p.get();
                })
                .map(Group::getId)
                .toList();

        AppState state = AppState.get();
        Task<ActionResponse> task = new Task<>() {
            @Override protected ActionResponse call() {
                return state.getGrpc().setUserPermissions(
                        selected.getUserId(), globalAccess, selectedGroupIds, state.getUserId());
            }
        };
        task.setOnSucceeded(e -> {
            ActionResponse resp = task.getValue();
            if (resp.getSuccess()) {
                loadData();
            } else {
                showError(resp.getErrorMessage());
            }
        });
        task.setOnFailed(e -> showError("Failed to save permissions."));
        bg(task);
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
