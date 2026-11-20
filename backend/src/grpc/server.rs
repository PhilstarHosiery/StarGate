use std::collections::HashMap;
use std::fmt::Display;
use std::sync::Arc;

use chrono::Utc;
use tonic::{Request, Response, Status};
use tracing::{error, info, warn};

use super::streams::{InboxStream, StreamManager};
use crate::db::{Db, DbError, format_timestamp};
use crate::models::Session;
use crate::password;
use crate::pb;
use crate::pb::star_gate_core_server::StarGateCore;
use crate::sms::OutboundClient;

type RpcResult<T> = Result<Response<T>, Status>;

/// Implements every RPC of the StarGateCore service.
pub struct StarGateService {
    db: Db,
    streams: Arc<StreamManager>,
    sms_out: Arc<OutboundClient>,
}

impl StarGateService {
    pub fn new(db: Db, streams: Arc<StreamManager>, sms_out: Arc<OutboundClient>) -> Self {
        Self {
            db,
            streams,
            sms_out,
        }
    }

    /// PermissionDenied unless the user has global access.
    async fn require_global_access(&self, user_id: &str) -> Result<(), Status> {
        match self.db.get_user_by_id(user_id).await {
            Ok(Some(user)) if user.has_global_access => Ok(()),
            Ok(_) => Err(Status::permission_denied("global access required")),
            Err(e) => Err(internal("requireGlobalAccess", e)),
        }
    }

    /// Builds the wire session, resolving the contact's group and name.
    async fn chat_session(&self, session: Session, op: &str) -> pb::ChatSession {
        let contact = match self.db.get_contact_by_phone(&session.contact_phone).await {
            Ok(contact) => contact,
            Err(e) => {
                warn!(phone = %session.contact_phone, err = %e, "{op}: contact lookup failed");
                None
            }
        };
        let (group_id, contact_name) = contact
            .map(|c| (c.group_id.unwrap_or_default(), c.name.unwrap_or_default()))
            .unwrap_or_default();
        pb::ChatSession {
            session_id: session.session_id,
            contact_phone: session.contact_phone,
            group_id,
            status: session.status,
            contact_name,
        }
    }

    async fn username(&self, user_id: &str) -> String {
        match self.db.get_user_by_id(user_id).await {
            Ok(Some(user)) => user.username,
            _ => String::new(),
        }
    }
}

/// Logs the underlying error and returns an opaque Internal status.
fn internal(op: &str, err: impl Display) -> Status {
    error!(err = %err, "{op}: error");
    Status::internal("internal error")
}

fn ok() -> pb::ActionResponse {
    pb::ActionResponse {
        success: true,
        error_message: String::new(),
    }
}

fn fail(msg: impl Into<String>) -> pb::ActionResponse {
    pb::ActionResponse {
        success: false,
        error_message: msg.into(),
    }
}

#[tonic::async_trait]
impl StarGateCore for StarGateService {
    /// Validates username/password and returns the user's ID on success.
    async fn login(&self, request: Request<pb::LoginRequest>) -> RpcResult<pb::LoginResponse> {
        let req = request.into_inner();
        let failure = |msg: &str| {
            Ok(Response::new(pb::LoginResponse {
                success: false,
                error_message: msg.to_string(),
                ..Default::default()
            }))
        };
        if req.username.is_empty() || req.password.is_empty() {
            return failure("username and password are required");
        }

        let user = match self.db.get_user_by_username(&req.username).await {
            Ok(Some(user)) => user,
            Ok(None) => return failure("invalid credentials"),
            Err(e) => return Err(internal("Login", e)),
        };
        if !password::verify(req.password, user.password_hash).await {
            return failure("invalid credentials");
        }

        info!(user_id = %user.user_id, username = %user.username, "Login: success");
        Ok(Response::new(pb::LoginResponse {
            success: true,
            user_id: user.user_id,
            error_message: String::new(),
            has_global_access: user.has_global_access,
        }))
    }

    /// Verifies the current password and replaces it with a new bcrypt hash.
    async fn change_password(
        &self,
        request: Request<pb::ChangePasswordRequest>,
    ) -> RpcResult<pb::ActionResponse> {
        let req = request.into_inner();
        if req.current_password.is_empty() || req.new_password.is_empty() {
            return Ok(Response::new(fail("current and new password are required")));
        }
        let user = match self.db.get_user_by_id(&req.user_id).await {
            Ok(Some(user)) => user,
            Ok(None) => return Err(Status::not_found("user not found")),
            Err(e) => return Err(internal("ChangePassword", e)),
        };
        if !password::verify(req.current_password, user.password_hash).await {
            return Ok(Response::new(fail("current password is incorrect")));
        }
        let hash = password::hash(req.new_password)
            .await
            .map_err(|e| internal("ChangePassword", e))?;
        self.db
            .update_password_hash(&req.user_id, &hash)
            .await
            .map_err(|e| internal("ChangePassword", e))?;
        info!(user_id = %req.user_id, "ChangePassword: success");
        Ok(Response::new(ok()))
    }

    type SubscribeToInboxStream = InboxStream;

    /// Registers the caller's stream; events flow until the client disconnects.
    async fn subscribe_to_inbox(
        &self,
        request: Request<pb::User>,
    ) -> RpcResult<Self::SubscribeToInboxStream> {
        let req = request.into_inner();
        if req.user_id.is_empty() {
            return Err(Status::invalid_argument("user_id is required"));
        }
        Ok(Response::new(self.streams.register(&req.user_id)))
    }

    /// All sessions the user has permission to see.
    async fn get_sessions(&self, request: Request<pb::User>) -> RpcResult<pb::SessionsResponse> {
        let req = request.into_inner();
        let sessions = self
            .db
            .get_sessions_by_user_access(&req.user_id)
            .await
            .map_err(|e| internal("GetSessions", e))?;

        let mut out = Vec::with_capacity(sessions.len());
        for session in sessions {
            out.push(self.chat_session(session, "GetSessions").await);
        }
        Ok(Response::new(pb::SessionsResponse { sessions: out }))
    }

    async fn get_session(
        &self,
        request: Request<pb::SessionRequest>,
    ) -> RpcResult<pb::ChatSession> {
        let req = request.into_inner();
        let session = self
            .db
            .get_session_by_id(&req.session_id)
            .await
            .map_err(|e| internal("GetSession", e))?
            .ok_or_else(|| Status::not_found(format!("session {:?} not found", req.session_id)))?;
        Ok(Response::new(
            self.chat_session(session, "GetSession").await,
        ))
    }

    async fn get_messages(
        &self,
        request: Request<pb::SessionRequest>,
    ) -> RpcResult<pb::MessagesResponse> {
        let req = request.into_inner();
        let messages = self
            .db
            .get_messages_by_session(&req.session_id)
            .await
            .map_err(|e| internal("GetMessages", e))?;

        let mut usernames: HashMap<String, String> = HashMap::new();
        let mut out = Vec::with_capacity(messages.len());
        for m in messages {
            let sent_by_username = match &m.sent_by_user_id {
                Some(id) => match usernames.get(id) {
                    Some(name) => name.clone(),
                    None => {
                        let name = self.username(id).await;
                        usernames.insert(id.clone(), name.clone());
                        name
                    }
                },
                None => String::new(),
            };
            out.push(pb::Message {
                message_id: m.message_id,
                session_id: m.session_id,
                direction: m.direction,
                text: m.text,
                sent_by_user_id: m.sent_by_user_id.unwrap_or_default(),
                timestamp: format_timestamp(m.timestamp),
                sent_by_username,
            });
        }
        Ok(Response::new(pb::MessagesResponse { messages: out }))
    }

    /// Stores an outbound message, sends it via the SMS gateway on the
    /// contact's sticky SIM, and broadcasts it to the other users who can see it.
    async fn send_reply(
        &self,
        request: Request<pb::ReplyRequest>,
    ) -> RpcResult<pb::ActionResponse> {
        let req = request.into_inner();
        if req.session_id.is_empty() || req.message_text.is_empty() || req.user_id.is_empty() {
            return Ok(Response::new(fail(
                "session_id, message_text and user_id are required",
            )));
        }

        let session = match self.db.get_session_by_id(&req.session_id).await {
            Ok(Some(session)) => session,
            Ok(None) => {
                return Ok(Response::new(fail(format!(
                    "session {:?} not found",
                    req.session_id
                ))));
            }
            Err(e) => return Err(internal("SendReply", e)),
        };
        let Ok(Some(contact)) = self.db.get_contact_by_phone(&session.contact_phone).await else {
            return Ok(Response::new(fail("contact not found")));
        };

        let now = Utc::now();
        self.db
            .create_message(
                &req.session_id,
                "OUTBOUND",
                &req.message_text,
                Some(&req.user_id),
                None,
                now,
            )
            .await
            .map_err(|e| internal("SendReply: CreateMessage", e))?;

        if let Err(e) = self
            .sms_out
            .send(
                &session.contact_phone,
                contact.assigned_sim,
                &req.message_text,
            )
            .await
        {
            error!(err = %format!("{e:#}"), phone = %session.contact_phone, "SendReply: SMS dispatch failed");
            return Ok(Response::new(fail(format!("SMS dispatch failed: {e:#}"))));
        }

        match self
            .db
            .get_users_with_access_to_group(contact.group_id.as_deref())
            .await
        {
            Err(e) => warn!(err = %e, "SendReply: GetUsersWithAccessToGroup failed"),
            Ok(targets) => {
                let others: Vec<String> = targets
                    .into_iter()
                    .filter(|id| *id != req.user_id)
                    .collect();
                let event = pb::MessageEvent {
                    session_id: req.session_id.clone(),
                    message_text: req.message_text.clone(),
                    sender_type: req.user_id.clone(),
                    timestamp: format_timestamp(now),
                    sender_username: self.username(&req.user_id).await,
                };
                self.streams.broadcast(&event, &others);
            }
        }

        Ok(Response::new(ok()))
    }

    /// Archives a contact whose number was reassigned and returns a fresh session.
    async fn retire_contact(
        &self,
        request: Request<pb::RetireRequest>,
    ) -> RpcResult<pb::ChatSession> {
        let req = request.into_inner();
        if req.contact_phone.is_empty() {
            return Err(Status::invalid_argument("contact_phone is required"));
        }
        let session = self
            .db
            .retire_contact(&req.contact_phone)
            .await
            .map_err(|e| internal("RetireContact", e))?;
        Ok(Response::new(pb::ChatSession {
            session_id: session.session_id,
            contact_phone: session.contact_phone,
            group_id: String::new(),
            status: session.status,
            contact_name: String::new(),
        }))
    }

    async fn rename_contact(
        &self,
        request: Request<pb::RenameRequest>,
    ) -> RpcResult<pb::ActionResponse> {
        let req = request.into_inner();
        if req.contact_phone.is_empty() || req.name.is_empty() {
            return Ok(Response::new(fail("contact_phone and name are required")));
        }
        self.db
            .rename_contact(&req.contact_phone, &req.name)
            .await
            .map_err(|e| internal("RenameContact", e))?;
        Ok(Response::new(ok()))
    }

    /// Moves a contact to a group and tells everyone who can now see it.
    async fn assign_contact(
        &self,
        request: Request<pb::AssignRequest>,
    ) -> RpcResult<pb::ActionResponse> {
        let req = request.into_inner();
        if req.contact_phone.is_empty() || req.group_id.is_empty() {
            return Ok(Response::new(fail(
                "contact_phone and group_id are required",
            )));
        }
        self.db
            .assign_contact(&req.contact_phone, &req.group_id)
            .await
            .map_err(|e| internal("AssignContact", e))?;

        let session = match self.db.get_open_session_by_phone(&req.contact_phone).await {
            Ok(session) => session,
            Err(e) => {
                warn!(err = %e, "AssignContact: GetOpenSessionByPhone failed");
                None
            }
        };
        if let Some(session) = session {
            match self
                .db
                .get_users_with_access_to_group(Some(&req.group_id))
                .await
            {
                Err(e) => warn!(err = %e, "AssignContact: GetUsersWithAccessToGroup failed"),
                Ok(targets) => {
                    let event = pb::MessageEvent {
                        session_id: session.session_id,
                        sender_type: "system".to_string(),
                        ..Default::default()
                    };
                    self.streams.broadcast(&event, &targets);
                }
            }
        }

        Ok(Response::new(ok()))
    }

    /// Groups accessible to the user (all of them for HR).
    async fn list_groups(&self, request: Request<pb::User>) -> RpcResult<pb::GroupsResponse> {
        let req = request.into_inner();
        let groups = self
            .db
            .list_groups_for_user(&req.user_id)
            .await
            .map_err(|e| internal("ListGroups", e))?;
        Ok(Response::new(pb::GroupsResponse {
            groups: groups
                .into_iter()
                .map(|g| pb::Group {
                    id: g.group_id,
                    name: g.group_name,
                })
                .collect(),
        }))
    }

    /// Creates a new contact and OPEN session for an outbound conversation.
    async fn create_session(
        &self,
        request: Request<pb::CreateSessionRequest>,
    ) -> RpcResult<pb::ChatSession> {
        let req = request.into_inner();
        if req.phone_number.is_empty() || req.group_id.is_empty() {
            return Err(Status::invalid_argument(
                "phone_number and group_id are required",
            ));
        }
        let allowed = self
            .db
            .user_has_access(&req.requesting_user_id, &req.group_id)
            .await
            .map_err(|e| internal("CreateSession: UserHasAccess", e))?;
        if !allowed {
            return Err(Status::permission_denied("no access to the selected group"));
        }

        let session = match self
            .db
            .start_outbound_session(&req.phone_number, &req.group_id, &req.contact_name)
            .await
        {
            Ok(session) => session,
            Err(DbError::ContactExists) => {
                return Err(Status::already_exists(
                    "a contact with that number already exists",
                ));
            }
            Err(e) => return Err(internal("CreateSession", e)),
        };
        Ok(Response::new(pb::ChatSession {
            session_id: session.session_id,
            contact_phone: session.contact_phone,
            group_id: req.group_id,
            status: session.status,
            contact_name: req.contact_name,
        }))
    }

    // -------------------------------------------------------------------------
    // Admin RPCs — require global access
    // -------------------------------------------------------------------------

    async fn list_users(&self, request: Request<pb::User>) -> RpcResult<pb::UsersResponse> {
        let req = request.into_inner();
        self.require_global_access(&req.user_id).await?;
        let users = self
            .db
            .list_users_with_groups()
            .await
            .map_err(|e| internal("ListUsers", e))?;
        Ok(Response::new(pb::UsersResponse {
            users: users
                .into_iter()
                .map(|u| pb::UserInfo {
                    user_id: u.user_id,
                    username: u.username,
                    has_global_access: u.has_global_access,
                    group_ids: u.group_ids,
                })
                .collect(),
        }))
    }

    async fn create_user(
        &self,
        request: Request<pb::CreateUserRequest>,
    ) -> RpcResult<pb::ActionResponse> {
        let req = request.into_inner();
        self.require_global_access(&req.requesting_user_id).await?;
        if req.username.is_empty() || req.password.is_empty() {
            return Ok(Response::new(fail("username and password are required")));
        }
        let hash = password::hash(req.password)
            .await
            .map_err(|e| internal("CreateUser", e))?;
        if let Err(e) = self
            .db
            .create_user(&req.username, &hash, req.has_global_access)
            .await
        {
            error!(err = %e, "CreateUser: db error");
            return Ok(Response::new(fail(format!("could not create user: {e}"))));
        }
        info!(username = %req.username, by = %req.requesting_user_id, "admin: CreateUser");
        Ok(Response::new(ok()))
    }

    async fn delete_user(
        &self,
        request: Request<pb::DeleteUserRequest>,
    ) -> RpcResult<pb::ActionResponse> {
        let req = request.into_inner();
        self.require_global_access(&req.requesting_user_id).await?;
        if req.user_id == req.requesting_user_id {
            return Ok(Response::new(fail("cannot delete your own account")));
        }
        self.db
            .delete_user(&req.user_id)
            .await
            .map_err(|e| internal("DeleteUser", e))?;
        info!(user_id = %req.user_id, by = %req.requesting_user_id, "admin: DeleteUser");
        Ok(Response::new(ok()))
    }

    async fn create_group(&self, request: Request<pb::CreateGroupRequest>) -> RpcResult<pb::Group> {
        let req = request.into_inner();
        self.require_global_access(&req.requesting_user_id).await?;
        if req.name.is_empty() {
            return Err(Status::invalid_argument("name is required"));
        }
        let group = self
            .db
            .create_group(&req.name)
            .await
            .map_err(|e| internal("CreateGroup", e))?;
        info!(name = %req.name, by = %req.requesting_user_id, "admin: CreateGroup");
        Ok(Response::new(pb::Group {
            id: group.group_id,
            name: group.group_name,
        }))
    }

    async fn rename_group(
        &self,
        request: Request<pb::RenameGroupRequest>,
    ) -> RpcResult<pb::ActionResponse> {
        let req = request.into_inner();
        self.require_global_access(&req.requesting_user_id).await?;
        if req.group_id.is_empty() || req.new_name.is_empty() {
            return Ok(Response::new(fail("group_id and new_name are required")));
        }
        self.db
            .rename_group(&req.group_id, &req.new_name)
            .await
            .map_err(|e| internal("RenameGroup", e))?;
        info!(group_id = %req.group_id, new_name = %req.new_name, by = %req.requesting_user_id, "admin: RenameGroup");
        Ok(Response::new(ok()))
    }

    async fn delete_group(
        &self,
        request: Request<pb::DeleteGroupRequest>,
    ) -> RpcResult<pb::ActionResponse> {
        let req = request.into_inner();
        self.require_global_access(&req.requesting_user_id).await?;
        if let Err(e) = self.db.delete_group(&req.group_id).await {
            error!(err = %e, "DeleteGroup: db error");
            return Ok(Response::new(fail(e.to_string())));
        }
        info!(group_id = %req.group_id, by = %req.requesting_user_id, "admin: DeleteGroup");
        Ok(Response::new(ok()))
    }

    async fn set_user_permissions(
        &self,
        request: Request<pb::SetPermissionsRequest>,
    ) -> RpcResult<pb::ActionResponse> {
        let req = request.into_inner();
        self.require_global_access(&req.requesting_user_id).await?;
        self.db
            .set_user_permissions(&req.user_id, &req.group_ids, req.has_global_access)
            .await
            .map_err(|e| internal("SetUserPermissions", e))?;
        info!(user_id = %req.user_id, by = %req.requesting_user_id, "admin: SetUserPermissions");
        Ok(Response::new(ok()))
    }
}
