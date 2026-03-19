CREATE TABLE IF NOT EXISTS users (
    user_id TEXT PRIMARY KEY,
    username TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    has_global_access INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS groups (
    group_id TEXT PRIMARY KEY,
    group_name TEXT UNIQUE NOT NULL
);

CREATE TABLE IF NOT EXISTS user_groups (
    user_id TEXT NOT NULL REFERENCES users(user_id),
    group_id TEXT NOT NULL REFERENCES groups(group_id),
    PRIMARY KEY (user_id, group_id)
);

CREATE TABLE IF NOT EXISTS contacts (
    contact_phone TEXT PRIMARY KEY,
    name TEXT,
    group_id TEXT REFERENCES groups(group_id),
    assigned_sim INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS sessions (
    session_id TEXT PRIMARY KEY,
    contact_phone TEXT NOT NULL REFERENCES contacts(contact_phone),
    status TEXT NOT NULL DEFAULT 'OPEN'
);

CREATE TABLE IF NOT EXISTS messages (
    message_id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(session_id),
    direction TEXT NOT NULL,
    text TEXT NOT NULL,
    sent_by_user_id TEXT REFERENCES users(user_id),
    timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
