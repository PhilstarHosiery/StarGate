//! All database queries. Migrations are embedded and run on startup.

use std::collections::{HashMap, HashSet};

use chrono::{DateTime, NaiveDateTime, SecondsFormat, Utc};
use sqlx::sqlite::{SqliteConnectOptions, SqliteJournalMode, SqlitePool, SqlitePoolOptions};

use crate::models::{Contact, Group, Message, Session, User, UserWithGroups};

/// Embedded migrations, applied in order and tracked in `schema_migrations`
/// (the same table the Go backend used, so existing databases carry over).
const MIGRATIONS: &[(&str, &str)] = &[
    (
        "001_initial.sql",
        include_str!("../migrations/001_initial.sql"),
    ),
    (
        "002_add_gateway_message_id.sql",
        include_str!("../migrations/002_add_gateway_message_id.sql"),
    ),
    (
        "003_add_fk_indexes.sql",
        include_str!("../migrations/003_add_fk_indexes.sql"),
    ),
];

#[derive(Debug, thiserror::Error)]
pub enum DbError {
    #[error("contact already exists")]
    ContactExists,
    #[error("cannot delete group: {0} contact(s) still assigned")]
    GroupNotEmpty(i64),
    #[error("user {0:?} not found")]
    UserNotFound(String),
    #[error(transparent)]
    Sql(#[from] sqlx::Error),
}

pub type Result<T> = std::result::Result<T, DbError>;

type UserRow = (String, String, String, i64);
type SessionRow = (String, String, String);
type ContactRow = (Option<String>, Option<String>, i64);
type GroupRow = (String, String);
type MessageRow = (String, String, String, String, Option<String>, String);

/// Cheap to clone; wraps a connection pool.
#[derive(Clone)]
pub struct Db {
    pool: SqlitePool,
}

impl Db {
    /// Opens (creating if needed) the SQLite database at path with WAL mode and
    /// foreign keys enabled on every connection.
    pub async fn open(path: &str) -> Result<Self> {
        let opts = SqliteConnectOptions::new()
            .filename(path)
            .create_if_missing(true)
            .journal_mode(SqliteJournalMode::Wal)
            .foreign_keys(true);
        let pool = SqlitePoolOptions::new().connect_with(opts).await?;
        Ok(Self { pool })
    }

    /// Fresh in-memory database. A single connection, since each in-memory
    /// connection is its own database.
    #[cfg(test)]
    pub async fn open_in_memory() -> Result<Self> {
        let opts = "sqlite::memory:"
            .parse::<SqliteConnectOptions>()?
            .foreign_keys(true);
        let pool = SqlitePoolOptions::new()
            .max_connections(1)
            .connect_with(opts)
            .await?;
        Ok(Self { pool })
    }

    /// Runs all embedded migrations in order, skipping already-applied ones.
    pub async fn migrate(&self) -> Result<()> {
        sqlx::query(
            "CREATE TABLE IF NOT EXISTS schema_migrations (
                version    TEXT PRIMARY KEY,
                applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
            )",
        )
        .execute(&self.pool)
        .await?;

        for &(version, sql) in MIGRATIONS {
            let (count,): (i64,) =
                sqlx::query_as("SELECT COUNT(*) FROM schema_migrations WHERE version = ?")
                    .bind(version)
                    .fetch_one(&self.pool)
                    .await?;
            if count > 0 {
                continue;
            }

            let mut tx = self.pool.begin().await?;
            sqlx::raw_sql(sql).execute(&mut *tx).await?;
            sqlx::query("INSERT INTO schema_migrations (version) VALUES (?)")
                .bind(version)
                .execute(&mut *tx)
                .await?;
            tx.commit().await?;
        }
        Ok(())
    }

    // -------------------------------------------------------------------------
    // Users
    // -------------------------------------------------------------------------

    /// Inserts a new user with a pre-hashed password and returns the new user_id.
    pub async fn create_user(
        &self,
        username: &str,
        password_hash: &str,
        global_access: bool,
    ) -> Result<String> {
        let id = new_id();
        sqlx::query(
            "INSERT INTO users (user_id, username, password_hash, has_global_access) VALUES (?, ?, ?, ?)",
        )
        .bind(&id)
        .bind(username)
        .bind(password_hash)
        .bind(global_access as i64)
        .execute(&self.pool)
        .await?;
        Ok(id)
    }

    pub async fn get_user_by_username(&self, username: &str) -> Result<Option<User>> {
        self.fetch_user(
            "SELECT user_id, username, password_hash, has_global_access FROM users WHERE username = ?",
            username,
        )
        .await
    }

    pub async fn get_user_by_id(&self, user_id: &str) -> Result<Option<User>> {
        self.fetch_user(
            "SELECT user_id, username, password_hash, has_global_access FROM users WHERE user_id = ?",
            user_id,
        )
        .await
    }

    async fn fetch_user(&self, sql: &str, arg: &str) -> Result<Option<User>> {
        let row: Option<UserRow> = sqlx::query_as(sql)
            .bind(arg)
            .fetch_optional(&self.pool)
            .await?;
        Ok(row.map(|(user_id, username, password_hash, ga)| User {
            user_id,
            username,
            password_hash,
            has_global_access: ga != 0,
        }))
    }

    /// Replaces the stored bcrypt hash for a user.
    pub async fn update_password_hash(&self, user_id: &str, new_hash: &str) -> Result<()> {
        sqlx::query("UPDATE users SET password_hash = ? WHERE user_id = ?")
            .bind(new_hash)
            .bind(user_id)
            .execute(&self.pool)
            .await?;
        Ok(())
    }

    /// True if the user can see group_id, either via a user_groups mapping or
    /// because they have has_global_access.
    pub async fn user_has_access(&self, user_id: &str, group_id: &str) -> Result<bool> {
        let (count,): (i64,) = sqlx::query_as(
            "SELECT COUNT(*) FROM users WHERE user_id = ? AND has_global_access = 1",
        )
        .bind(user_id)
        .fetch_one(&self.pool)
        .await?;
        if count > 0 {
            return Ok(true);
        }

        let (count,): (i64,) =
            sqlx::query_as("SELECT COUNT(*) FROM user_groups WHERE user_id = ? AND group_id = ?")
                .bind(user_id)
                .bind(group_id)
                .fetch_one(&self.pool)
                .await?;
        Ok(count > 0)
    }

    // -------------------------------------------------------------------------
    // Sessions
    // -------------------------------------------------------------------------

    /// All sessions the user may see, most recent activity first.
    /// Global-access users see every session; others see sessions for contacts
    /// in their groups.
    pub async fn get_sessions_by_user_access(&self, user_id: &str) -> Result<Vec<Session>> {
        let user = self
            .get_user_by_id(user_id)
            .await?
            .ok_or_else(|| DbError::UserNotFound(user_id.to_string()))?;

        let rows: Vec<SessionRow> = if user.has_global_access {
            sqlx::query_as(
                "SELECT s.session_id, s.contact_phone, s.status
                 FROM sessions s
                 LEFT JOIN (
                   SELECT session_id, MAX(timestamp) AS last_at
                   FROM messages GROUP BY session_id
                 ) m ON m.session_id = s.session_id
                 ORDER BY m.last_at DESC NULLS LAST",
            )
            .fetch_all(&self.pool)
            .await?
        } else {
            sqlx::query_as(
                "SELECT s.session_id, s.contact_phone, s.status
                 FROM sessions s
                 JOIN contacts c ON c.contact_phone = s.contact_phone
                 LEFT JOIN (
                   SELECT session_id, MAX(timestamp) AS last_at
                   FROM messages GROUP BY session_id
                 ) m ON m.session_id = s.session_id
                 WHERE c.group_id IN (
                   SELECT group_id FROM user_groups WHERE user_id = ?
                 )
                 ORDER BY m.last_at DESC NULLS LAST",
            )
            .bind(user_id)
            .fetch_all(&self.pool)
            .await?
        };
        Ok(rows.into_iter().map(session_from_row).collect())
    }

    pub async fn get_session_by_id(&self, session_id: &str) -> Result<Option<Session>> {
        let row: Option<SessionRow> = sqlx::query_as(
            "SELECT session_id, contact_phone, status FROM sessions WHERE session_id = ?",
        )
        .bind(session_id)
        .fetch_optional(&self.pool)
        .await?;
        Ok(row.map(session_from_row))
    }

    /// The open session for a contact, if any.
    pub async fn get_open_session_by_phone(&self, contact_phone: &str) -> Result<Option<Session>> {
        let row: Option<SessionRow> = sqlx::query_as(
            "SELECT session_id, contact_phone, status FROM sessions
             WHERE contact_phone = ? AND status = 'OPEN'
             LIMIT 1",
        )
        .bind(contact_phone)
        .fetch_optional(&self.pool)
        .await?;
        Ok(row.map(session_from_row))
    }

    /// Creates a new OPEN session for the given contact phone.
    pub async fn create_session(&self, contact_phone: &str) -> Result<Session> {
        let session = Session {
            session_id: new_id(),
            contact_phone: contact_phone.to_string(),
            status: "OPEN".to_string(),
        };
        sqlx::query("INSERT INTO sessions (session_id, contact_phone, status) VALUES (?, ?, ?)")
            .bind(&session.session_id)
            .bind(&session.contact_phone)
            .bind(&session.status)
            .execute(&self.pool)
            .await?;
        Ok(session)
    }

    // -------------------------------------------------------------------------
    // Messages
    // -------------------------------------------------------------------------

    /// All messages for a session, oldest first.
    pub async fn get_messages_by_session(&self, session_id: &str) -> Result<Vec<Message>> {
        let rows: Vec<MessageRow> = sqlx::query_as(
            "SELECT message_id, session_id, direction, text, sent_by_user_id, CAST(timestamp AS TEXT)
             FROM messages WHERE session_id = ? ORDER BY timestamp ASC",
        )
        .bind(session_id)
        .fetch_all(&self.pool)
        .await?;
        Ok(rows
            .into_iter()
            .map(
                |(message_id, session_id, direction, text, sent_by_user_id, ts)| Message {
                    message_id,
                    session_id,
                    direction,
                    text,
                    sent_by_user_id,
                    timestamp: parse_timestamp(&ts),
                },
            )
            .collect())
    }

    /// Inserts a new message. `sent_by_user_id` is None for inbound messages.
    /// `gateway_message_id` is the SMS Gate message ID used to deduplicate
    /// webhook retries (None for outbound). `timestamp` is the authoritative
    /// message time. Returns Ok(None) if the message was already recorded.
    pub async fn create_message(
        &self,
        session_id: &str,
        direction: &str,
        text: &str,
        sent_by_user_id: Option<&str>,
        gateway_message_id: Option<&str>,
        timestamp: DateTime<Utc>,
    ) -> Result<Option<Message>> {
        let message = Message {
            message_id: new_id(),
            session_id: session_id.to_string(),
            direction: direction.to_string(),
            text: text.to_string(),
            sent_by_user_id: sent_by_user_id.map(str::to_string),
            timestamp,
        };
        let result = sqlx::query(
            "INSERT OR IGNORE INTO messages
               (message_id, session_id, direction, text, sent_by_user_id, timestamp, gateway_message_id)
             VALUES (?, ?, ?, ?, ?, ?, ?)",
        )
        .bind(&message.message_id)
        .bind(&message.session_id)
        .bind(&message.direction)
        .bind(&message.text)
        .bind(sent_by_user_id)
        .bind(format_timestamp(timestamp))
        .bind(gateway_message_id)
        .execute(&self.pool)
        .await?;
        if result.rows_affected() == 0 {
            return Ok(None); // duplicate — already processed
        }
        Ok(Some(message))
    }

    // -------------------------------------------------------------------------
    // Contacts
    // -------------------------------------------------------------------------

    pub async fn get_contact_by_phone(&self, phone: &str) -> Result<Option<Contact>> {
        let row: Option<ContactRow> = sqlx::query_as(
            "SELECT name, group_id, assigned_sim FROM contacts WHERE contact_phone = ?",
        )
        .bind(phone)
        .fetch_optional(&self.pool)
        .await?;
        Ok(row.map(|(name, group_id, assigned_sim)| Contact {
            name,
            group_id,
            assigned_sim,
        }))
    }

    /// Inserts an unknown contact (no name, no group) with the given SIM.
    pub async fn create_contact(&self, phone: &str, assigned_sim: i64) -> Result<Contact> {
        sqlx::query(
            "INSERT INTO contacts (contact_phone, name, group_id, assigned_sim) VALUES (?, NULL, NULL, ?)",
        )
        .bind(phone)
        .bind(assigned_sim)
        .execute(&self.pool)
        .await?;
        Ok(Contact {
            name: None,
            group_id: None,
            assigned_sim,
        })
    }

    pub async fn rename_contact(&self, phone: &str, name: &str) -> Result<()> {
        sqlx::query("UPDATE contacts SET name = ? WHERE contact_phone = ?")
            .bind(name)
            .bind(phone)
            .execute(&self.pool)
            .await?;
        Ok(())
    }

    pub async fn assign_contact(&self, phone: &str, group_id: &str) -> Result<()> {
        sqlx::query("UPDATE contacts SET group_id = ? WHERE contact_phone = ?")
            .bind(group_id)
            .bind(phone)
            .execute(&self.pool)
            .await?;
        Ok(())
    }

    /// Archives the contact under phone+"-old" (with its sessions, the open one
    /// closed), then creates a new unknown contact and OPEN session for the same
    /// number. Returns the new session.
    pub async fn retire_contact(&self, phone: &str) -> Result<Session> {
        let old_phone = format!("{phone}-old");
        let mut tx = self.pool.begin().await?;

        // Sessions are re-pointed before the contact is renamed, so FK checks
        // must wait until commit. Resets automatically when the tx ends.
        sqlx::query("PRAGMA defer_foreign_keys = ON")
            .execute(&mut *tx)
            .await?;

        sqlx::query(
            "UPDATE sessions SET status = 'CLOSED' WHERE contact_phone = ? AND status = 'OPEN'",
        )
        .bind(phone)
        .execute(&mut *tx)
        .await?;
        sqlx::query("UPDATE sessions SET contact_phone = ? WHERE contact_phone = ?")
            .bind(&old_phone)
            .bind(phone)
            .execute(&mut *tx)
            .await?;
        sqlx::query("UPDATE contacts SET contact_phone = ? WHERE contact_phone = ?")
            .bind(&old_phone)
            .bind(phone)
            .execute(&mut *tx)
            .await?;
        sqlx::query(
            "INSERT INTO contacts (contact_phone, name, group_id, assigned_sim) VALUES (?, NULL, NULL, 0)",
        )
        .bind(phone)
        .execute(&mut *tx)
        .await?;

        let session = Session {
            session_id: new_id(),
            contact_phone: phone.to_string(),
            status: "OPEN".to_string(),
        };
        sqlx::query(
            "INSERT INTO sessions (session_id, contact_phone, status) VALUES (?, ?, 'OPEN')",
        )
        .bind(&session.session_id)
        .bind(&session.contact_phone)
        .execute(&mut *tx)
        .await?;

        tx.commit().await?;
        Ok(session)
    }

    /// Creates a new contact and an OPEN session for an outbound conversation.
    /// Fails with ContactExists if the phone number is already in use.
    pub async fn start_outbound_session(
        &self,
        phone: &str,
        group_id: &str,
        name: &str,
    ) -> Result<Session> {
        if self.get_contact_by_phone(phone).await?.is_some() {
            return Err(DbError::ContactExists);
        }
        let name = (!name.is_empty()).then_some(name);
        sqlx::query(
            "INSERT INTO contacts (contact_phone, name, group_id, assigned_sim) VALUES (?, ?, ?, 0)",
        )
        .bind(phone)
        .bind(name)
        .bind(group_id)
        .execute(&self.pool)
        .await?;
        self.create_session(phone).await
    }

    // -------------------------------------------------------------------------
    // Groups
    // -------------------------------------------------------------------------

    /// All groups ordered by name.
    pub async fn list_groups(&self) -> Result<Vec<Group>> {
        let rows: Vec<GroupRow> =
            sqlx::query_as("SELECT group_id, group_name FROM groups ORDER BY group_name")
                .fetch_all(&self.pool)
                .await?;
        Ok(rows.into_iter().map(group_from_row).collect())
    }

    /// Groups the user has access to: all of them for global-access users,
    /// otherwise only those in the user's user_groups mapping.
    pub async fn list_groups_for_user(&self, user_id: &str) -> Result<Vec<Group>> {
        if let Some(user) = self.get_user_by_id(user_id).await?
            && user.has_global_access
        {
            return self.list_groups().await;
        }
        let rows: Vec<GroupRow> = sqlx::query_as(
            "SELECT g.group_id, g.group_name FROM groups g
             JOIN user_groups ug ON ug.group_id = g.group_id
             WHERE ug.user_id = ?
             ORDER BY g.group_name",
        )
        .bind(user_id)
        .fetch_all(&self.pool)
        .await?;
        Ok(rows.into_iter().map(group_from_row).collect())
    }

    /// User IDs of everyone who can see a group: all global-access users plus
    /// users mapped to the group. With no group, only global-access users (HR).
    pub async fn get_users_with_access_to_group(
        &self,
        group_id: Option<&str>,
    ) -> Result<Vec<String>> {
        let mut user_ids: Vec<String> =
            sqlx::query_scalar("SELECT user_id FROM users WHERE has_global_access = 1")
                .fetch_all(&self.pool)
                .await?;

        let Some(group_id) = group_id else {
            return Ok(user_ids);
        };

        let members: Vec<String> =
            sqlx::query_scalar("SELECT user_id FROM user_groups WHERE group_id = ?")
                .bind(group_id)
                .fetch_all(&self.pool)
                .await?;
        let mut seen: HashSet<String> = user_ids.iter().cloned().collect();
        for id in members {
            if seen.insert(id.clone()) {
                user_ids.push(id);
            }
        }
        Ok(user_ids)
    }

    pub async fn create_group(&self, name: &str) -> Result<Group> {
        let group = Group {
            group_id: new_id(),
            group_name: name.to_string(),
        };
        sqlx::query("INSERT INTO groups (group_id, group_name) VALUES (?, ?)")
            .bind(&group.group_id)
            .bind(&group.group_name)
            .execute(&self.pool)
            .await?;
        Ok(group)
    }

    pub async fn rename_group(&self, group_id: &str, name: &str) -> Result<()> {
        sqlx::query("UPDATE groups SET group_name = ? WHERE group_id = ?")
            .bind(name)
            .bind(group_id)
            .execute(&self.pool)
            .await?;
        Ok(())
    }

    /// Deletes a group and its user mappings. Refuses while contacts are assigned to it.
    pub async fn delete_group(&self, group_id: &str) -> Result<()> {
        let (count,): (i64,) = sqlx::query_as("SELECT COUNT(*) FROM contacts WHERE group_id = ?")
            .bind(group_id)
            .fetch_one(&self.pool)
            .await?;
        if count > 0 {
            return Err(DbError::GroupNotEmpty(count));
        }

        let mut tx = self.pool.begin().await?;
        sqlx::query("DELETE FROM user_groups WHERE group_id = ?")
            .bind(group_id)
            .execute(&mut *tx)
            .await?;
        sqlx::query("DELETE FROM groups WHERE group_id = ?")
            .bind(group_id)
            .execute(&mut *tx)
            .await?;
        tx.commit().await?;
        Ok(())
    }

    // -------------------------------------------------------------------------
    // Admin
    // -------------------------------------------------------------------------

    /// All users with their assigned group IDs.
    pub async fn list_users_with_groups(&self) -> Result<Vec<UserWithGroups>> {
        let users: Vec<(String, String, i64)> = sqlx::query_as(
            "SELECT user_id, username, has_global_access FROM users ORDER BY username",
        )
        .fetch_all(&self.pool)
        .await?;
        let mappings: Vec<(String, String)> =
            sqlx::query_as("SELECT user_id, group_id FROM user_groups ORDER BY group_id")
                .fetch_all(&self.pool)
                .await?;

        let mut groups_by_user: HashMap<String, Vec<String>> = HashMap::new();
        for (user_id, group_id) in mappings {
            groups_by_user.entry(user_id).or_default().push(group_id);
        }

        Ok(users
            .into_iter()
            .map(|(user_id, username, ga)| UserWithGroups {
                group_ids: groups_by_user.remove(&user_id).unwrap_or_default(),
                user_id,
                username,
                has_global_access: ga != 0,
            })
            .collect())
    }

    /// Removes a user and their group mappings.
    pub async fn delete_user(&self, user_id: &str) -> Result<()> {
        let mut tx = self.pool.begin().await?;
        sqlx::query("DELETE FROM user_groups WHERE user_id = ?")
            .bind(user_id)
            .execute(&mut *tx)
            .await?;
        sqlx::query("DELETE FROM users WHERE user_id = ?")
            .bind(user_id)
            .execute(&mut *tx)
            .await?;
        tx.commit().await?;
        Ok(())
    }

    /// Atomically replaces a user's has_global_access flag and group memberships.
    pub async fn set_user_permissions(
        &self,
        user_id: &str,
        group_ids: &[String],
        has_global_access: bool,
    ) -> Result<()> {
        let mut tx = self.pool.begin().await?;
        sqlx::query("UPDATE users SET has_global_access = ? WHERE user_id = ?")
            .bind(has_global_access as i64)
            .bind(user_id)
            .execute(&mut *tx)
            .await?;
        sqlx::query("DELETE FROM user_groups WHERE user_id = ?")
            .bind(user_id)
            .execute(&mut *tx)
            .await?;
        for group_id in group_ids {
            sqlx::query("INSERT INTO user_groups (user_id, group_id) VALUES (?, ?)")
                .bind(user_id)
                .bind(group_id)
                .execute(&mut *tx)
                .await?;
        }
        tx.commit().await?;
        Ok(())
    }
}

fn session_from_row((session_id, contact_phone, status): SessionRow) -> Session {
    Session {
        session_id,
        contact_phone,
        status,
    }
}

fn group_from_row((group_id, group_name): GroupRow) -> Group {
    Group {
        group_id,
        group_name,
    }
}

/// Random 16-byte ID as 32 hex chars.
fn new_id() -> String {
    hex::encode(rand::random::<[u8; 16]>())
}

/// RFC 3339 in UTC with whole seconds, e.g. "2026-01-02T15:04:05Z".
/// This is the storage and wire format for every timestamp.
pub fn format_timestamp(t: DateTime<Utc>) -> String {
    t.to_rfc3339_opts(SecondsFormat::Secs, true)
}

/// Parses a stored timestamp. Legacy rows were stored without a timezone as
/// "2006-01-02 15:04:05"; they are treated as UTC.
fn parse_timestamp(ts: &str) -> DateTime<Utc> {
    DateTime::parse_from_rfc3339(ts)
        .map(|t| t.with_timezone(&Utc))
        .or_else(|_| NaiveDateTime::parse_from_str(ts, "%Y-%m-%d %H:%M:%S").map(|t| t.and_utc()))
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    async fn test_db() -> Db {
        let db = Db::open_in_memory().await.unwrap();
        db.migrate().await.unwrap();
        db
    }

    #[test]
    fn timestamps_round_trip() {
        let t = parse_timestamp("2026-03-04T05:06:07Z");
        assert_eq!(format_timestamp(t), "2026-03-04T05:06:07Z");
        assert_eq!(
            format_timestamp(parse_timestamp("2026-03-04 05:06:07")),
            "2026-03-04T05:06:07Z"
        );
        assert_eq!(
            format_timestamp(parse_timestamp("2026-03-04T13:06:07+08:00")),
            "2026-03-04T05:06:07Z"
        );
    }

    #[tokio::test]
    async fn migrate_is_idempotent() {
        let db = test_db().await;
        db.migrate().await.unwrap();
        let (count,): (i64,) = sqlx::query_as("SELECT COUNT(*) FROM schema_migrations")
            .fetch_one(&db.pool)
            .await
            .unwrap();
        assert_eq!(count, MIGRATIONS.len() as i64);
    }

    #[tokio::test]
    async fn duplicate_gateway_message_is_ignored() {
        let db = test_db().await;
        db.create_contact("+639170000000", 1).await.unwrap();
        let session = db.create_session("+639170000000").await.unwrap();
        let now = Utc::now();
        let first = db
            .create_message(
                &session.session_id,
                "INBOUND",
                "hi",
                None,
                Some("gw-1"),
                now,
            )
            .await
            .unwrap();
        let second = db
            .create_message(
                &session.session_id,
                "INBOUND",
                "hi",
                None,
                Some("gw-1"),
                now,
            )
            .await
            .unwrap();
        assert!(first.is_some());
        assert!(second.is_none());
        assert_eq!(
            db.get_messages_by_session(&session.session_id)
                .await
                .unwrap()
                .len(),
            1
        );
    }

    #[tokio::test]
    async fn retire_contact_archives_old_and_creates_fresh() {
        let db = test_db().await;
        let group = db.create_group("Welding").await.unwrap();
        let phone = "+639170000001";
        db.start_outbound_session(phone, &group.group_id, "Juan")
            .await
            .unwrap();

        let fresh = db.retire_contact(phone).await.unwrap();

        let old = db
            .get_contact_by_phone(&format!("{phone}-old"))
            .await
            .unwrap()
            .unwrap();
        assert_eq!(old.name.as_deref(), Some("Juan"));
        let new = db.get_contact_by_phone(phone).await.unwrap().unwrap();
        assert_eq!(new.name, None);
        assert_eq!(new.group_id, None);
        let open = db.get_open_session_by_phone(phone).await.unwrap().unwrap();
        assert_eq!(open.session_id, fresh.session_id);
        assert!(
            db.get_open_session_by_phone(&format!("{phone}-old"))
                .await
                .unwrap()
                .is_none()
        );
    }

    #[tokio::test]
    async fn access_follows_groups_and_global_flag() {
        let db = test_db().await;
        let welding = db.create_group("Welding").await.unwrap();
        let logistics = db.create_group("Logistics").await.unwrap();
        let hr = db.create_user("hr", "x", true).await.unwrap();
        let sup = db.create_user("sup", "x", false).await.unwrap();
        db.set_user_permissions(&sup, std::slice::from_ref(&welding.group_id), false)
            .await
            .unwrap();

        assert!(db.user_has_access(&hr, &logistics.group_id).await.unwrap());
        assert!(db.user_has_access(&sup, &welding.group_id).await.unwrap());
        assert!(!db.user_has_access(&sup, &logistics.group_id).await.unwrap());

        let welders = db
            .get_users_with_access_to_group(Some(&welding.group_id))
            .await
            .unwrap();
        assert_eq!(welders, [hr.clone(), sup.clone()]);
        assert_eq!(db.get_users_with_access_to_group(None).await.unwrap(), [hr]);

        let groups = db.list_groups_for_user(&sup).await.unwrap();
        assert_eq!(groups.len(), 1);
    }

    #[tokio::test]
    async fn delete_group_refuses_while_contacts_assigned() {
        let db = test_db().await;
        let group = db.create_group("Welding").await.unwrap();
        db.start_outbound_session("+639170000002", &group.group_id, "")
            .await
            .unwrap();
        assert!(matches!(
            db.delete_group(&group.group_id).await,
            Err(DbError::GroupNotEmpty(1))
        ));
    }
}
