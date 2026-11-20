-- Indexes on foreign key columns for query performance.
-- SQLite does not automatically create indexes for FK columns.

-- user_groups: composite PK (user_id, group_id) covers user_id-first lookups,
-- but not group_id-first (e.g. "which users belong to group X").
CREATE INDEX IF NOT EXISTS idx_user_groups_group_id
    ON user_groups(group_id);

-- contacts: group_id — "which contacts are in group X"
CREATE INDEX IF NOT EXISTS idx_contacts_group_id
    ON contacts(group_id);

-- sessions: contact_phone — "all sessions for a given contact"
CREATE INDEX IF NOT EXISTS idx_sessions_contact_phone
    ON sessions(contact_phone);

-- messages: session_id — "all messages in a session" (primary query path)
CREATE INDEX IF NOT EXISTS idx_messages_session_id
    ON messages(session_id);

-- messages: sent_by_user_id — "messages sent by a given user"
CREATE INDEX IF NOT EXISTS idx_messages_sent_by_user_id
    ON messages(sent_by_user_id);
