package com.philstar.stargate;

import com.philstar.stargate.proto.*;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;
import io.grpc.stub.StreamObserver;

import java.util.List;

public class GrpcClient {

    private static final String HOST = "localhost";
    private static final int PORT = 50051;

    private final ManagedChannel channel;
    private final StarGateCoreGrpc.StarGateCoreBlockingStub blocking;
    private final StarGateCoreGrpc.StarGateCoreStub async;

    public GrpcClient() {
        channel = ManagedChannelBuilder.forAddress(HOST, PORT)
                .usePlaintext()
                .build();
        blocking = StarGateCoreGrpc.newBlockingStub(channel);
        async    = StarGateCoreGrpc.newStub(channel);
    }

    public LoginResponse login(String username, String password) {
        return blocking.login(LoginRequest.newBuilder()
                .setUsername(username)
                .setPassword(password)
                .build());
    }

    public List<ChatSession> getSessions(String userId) {
        return blocking.getSessions(User.newBuilder().setUserId(userId).build())
                .getSessionsList();
    }

    public ChatSession getSession(String sessionId) {
        return blocking.getSession(SessionRequest.newBuilder()
                .setSessionId(sessionId)
                .build());
    }

    public List<Message> getMessages(String sessionId) {
        return blocking.getMessages(SessionRequest.newBuilder()
                .setSessionId(sessionId)
                .build())
                .getMessagesList();
    }

    public ActionResponse sendReply(String sessionId, String text, String userId) {
        return blocking.sendReply(ReplyRequest.newBuilder()
                .setSessionId(sessionId)
                .setMessageText(text)
                .setUserId(userId)
                .build());
    }

    public ActionResponse renameContact(String phone, String name, String userId) {
        return blocking.renameContact(RenameRequest.newBuilder()
                .setContactPhone(phone)
                .setName(name)
                .setUserId(userId)
                .build());
    }

    public ActionResponse assignContact(String phone, String groupId, String userId) {
        return blocking.assignContact(AssignRequest.newBuilder()
                .setContactPhone(phone)
                .setGroupId(groupId)
                .setUserId(userId)
                .build());
    }

    public ChatSession retireContact(String phone, String userId) {
        return blocking.retireContact(RetireRequest.newBuilder()
                .setContactPhone(phone)
                .setUserId(userId)
                .build());
    }

    public List<Group> listGroups(String userId) {
        return blocking.listGroups(User.newBuilder().setUserId(userId).build())
                .getGroupsList();
    }

    public ChatSession createSession(String phone, String groupId, String contactName, String requestingUserId) {
        return blocking.createSession(CreateSessionRequest.newBuilder()
                .setPhoneNumber(phone)
                .setGroupId(groupId)
                .setContactName(contactName)
                .setRequestingUserId(requestingUserId)
                .build());
    }

    // -------------------------------------------------------------------------
    // Admin
    // -------------------------------------------------------------------------

    public List<UserInfo> listUsers(String requestingUserId) {
        return blocking.listUsers(User.newBuilder().setUserId(requestingUserId).build())
                .getUsersList();
    }

    public ActionResponse createUser(String username, String password, boolean hasGlobalAccess, String requestingUserId) {
        return blocking.createUser(CreateUserRequest.newBuilder()
                .setUsername(username)
                .setPassword(password)
                .setHasGlobalAccess(hasGlobalAccess)
                .setRequestingUserId(requestingUserId)
                .build());
    }

    public ActionResponse deleteUser(String userId, String requestingUserId) {
        return blocking.deleteUser(DeleteUserRequest.newBuilder()
                .setUserId(userId)
                .setRequestingUserId(requestingUserId)
                .build());
    }

    public Group createGroup(String name, String requestingUserId) {
        return blocking.createGroup(CreateGroupRequest.newBuilder()
                .setName(name)
                .setRequestingUserId(requestingUserId)
                .build());
    }

    public ActionResponse renameGroup(String groupId, String newName, String requestingUserId) {
        return blocking.renameGroup(RenameGroupRequest.newBuilder()
                .setGroupId(groupId)
                .setNewName(newName)
                .setRequestingUserId(requestingUserId)
                .build());
    }

    public ActionResponse deleteGroup(String groupId, String requestingUserId) {
        return blocking.deleteGroup(DeleteGroupRequest.newBuilder()
                .setGroupId(groupId)
                .setRequestingUserId(requestingUserId)
                .build());
    }

    public ActionResponse setUserPermissions(String userId, boolean hasGlobalAccess,
                                              List<String> groupIds, String requestingUserId) {
        return blocking.setUserPermissions(SetPermissionsRequest.newBuilder()
                .setUserId(userId)
                .setHasGlobalAccess(hasGlobalAccess)
                .addAllGroupIds(groupIds)
                .setRequestingUserId(requestingUserId)
                .build());
    }

    public void subscribeToInbox(String userId, StreamObserver<MessageEvent> observer) {
        async.subscribeToInbox(User.newBuilder()
                .setUserId(userId)
                .build(), observer);
    }

    public void shutdown() {
        channel.shutdownNow();
    }
}
