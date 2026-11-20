use chrono::{DateTime, Utc};

/// A JavaFX operator who logs into the system.
#[derive(Debug, Clone)]
pub struct User {
    pub user_id: String,
    pub username: String,
    pub password_hash: String,
    pub has_global_access: bool,
}

/// Anyone who texts the system (employee, supplier, unknown).
/// Identified by phone number; name and group can be assigned later.
#[derive(Debug, Clone)]
pub struct Contact {
    pub name: Option<String>,
    pub group_id: Option<String>,
    pub assigned_sim: i64,
}

/// A category that Contacts belong to (e.g. "Welding").
#[derive(Debug, Clone)]
pub struct Group {
    pub group_id: String,
    pub group_name: String,
}

/// An ongoing conversation thread with a Contact.
#[derive(Debug, Clone)]
pub struct Session {
    pub session_id: String,
    pub contact_phone: String,
    pub status: String, // "OPEN" | "CLOSED"
}

/// A User plus their assigned group IDs, used for admin display.
#[derive(Debug, Clone)]
pub struct UserWithGroups {
    pub user_id: String,
    pub username: String,
    pub has_global_access: bool,
    pub group_ids: Vec<String>,
}

/// A single text sent or received within a Session.
#[derive(Debug, Clone)]
pub struct Message {
    pub message_id: String,
    pub session_id: String,
    pub direction: String, // "INBOUND" | "OUTBOUND"
    pub text: String,
    pub sent_by_user_id: Option<String>,
    pub timestamp: DateTime<Utc>,
}
