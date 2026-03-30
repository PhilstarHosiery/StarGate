package db

import (
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/PhilstarHosiery/stargate/backend/internal/models"
	_ "modernc.org/sqlite"
)

//go:embed migrations
var migrationsFS embed.FS


// DB wraps a *sql.DB and exposes domain-level query methods.
type DB struct {
	sql *sql.DB
}

// Open opens the SQLite database at path with WAL mode enabled.
func Open(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("db: open %q: %w", path, err)
	}

	// Enable WAL mode for better concurrent read performance.
	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("db: enable WAL: %w", err)
	}

	// Enable foreign key enforcement.
	if _, err := sqlDB.Exec("PRAGMA foreign_keys=ON;"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("db: enable foreign keys: %w", err)
	}

	return &DB{sql: sqlDB}, nil
}

// Close closes the underlying database connection.
func (d *DB) Close() error {
	return d.sql.Close()
}

// Migrate runs all embedded migration files in order, skipping already-applied ones.
func Migrate(d *DB) error {
	// Ensure the tracking table exists.
	if _, err := d.sql.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("db: create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("db: read migrations dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version := entry.Name()

		var count int
		if err := d.sql.QueryRow(
			`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version,
		).Scan(&count); err != nil {
			return fmt.Errorf("db: check migration %s: %w", version, err)
		}
		if count > 0 {
			continue
		}

		content, err := fs.ReadFile(migrationsFS, "migrations/"+version)
		if err != nil {
			return fmt.Errorf("db: read migration %s: %w", version, err)
		}
		if _, err := d.sql.Exec(string(content)); err != nil {
			return fmt.Errorf("db: run migration %s: %w", version, err)
		}
		if _, err := d.sql.Exec(
			`INSERT INTO schema_migrations (version) VALUES (?)`, version,
		); err != nil {
			return fmt.Errorf("db: record migration %s: %w", version, err)
		}
	}
	return nil
}

// newID generates a short random hex ID (16 bytes = 32 hex chars).
func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("db: newID rand.Read failed: %v", err))
	}
	return hex.EncodeToString(b)
}

// -----------------------------------------------------------------------------
// Users
// -----------------------------------------------------------------------------

// CreateUser inserts a new user with a pre-hashed password and returns the new user_id.
func (d *DB) CreateUser(username, passwordHash string, globalAccess bool) (string, error) {
	id := newID()
	ga := 0
	if globalAccess {
		ga = 1
	}
	_, err := d.sql.Exec(
		`INSERT INTO users (user_id, username, password_hash, has_global_access) VALUES (?, ?, ?, ?)`,
		id, username, passwordHash, ga,
	)
	if err != nil {
		return "", fmt.Errorf("db: CreateUser: %w", err)
	}
	return id, nil
}

// GetUserByUsername fetches a user by their username.
func (d *DB) GetUserByUsername(username string) (*models.User, error) {
	row := d.sql.QueryRow(
		`SELECT user_id, username, password_hash, has_global_access FROM users WHERE username = ?`,
		username,
	)
	return scanUser(row)
}

// GetUserByID fetches a user by their user_id.
func (d *DB) GetUserByID(userID string) (*models.User, error) {
	row := d.sql.QueryRow(
		`SELECT user_id, username, password_hash, has_global_access FROM users WHERE user_id = ?`,
		userID,
	)
	return scanUser(row)
}

func scanUser(row *sql.Row) (*models.User, error) {
	var u models.User
	var globalAccess int
	if err := row.Scan(&u.UserID, &u.Username, &u.PasswordHash, &globalAccess); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("db: scan user: %w", err)
	}
	u.HasGlobalAccess = globalAccess != 0
	return &u, nil
}

// UserHasAccess returns true if the user has access to the given groupID,
// either via a user_groups mapping or because they have has_global_access.
func (d *DB) UserHasAccess(userID, groupID string) (bool, error) {
	var count int
	err := d.sql.QueryRow(
		`SELECT COUNT(*) FROM users WHERE user_id = ? AND has_global_access = 1`,
		userID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("db: UserHasAccess global check: %w", err)
	}
	if count > 0 {
		return true, nil
	}

	err = d.sql.QueryRow(
		`SELECT COUNT(*) FROM user_groups WHERE user_id = ? AND group_id = ?`,
		userID, groupID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("db: UserHasAccess group check: %w", err)
	}
	return count > 0, nil
}

// -----------------------------------------------------------------------------
// Sessions
// -----------------------------------------------------------------------------

// GetSessionsByUserAccess returns all sessions the user has permission to see.
// Global-access users see all sessions. Others see sessions for their groups
// plus sessions for contacts with no group assigned.
func (d *DB) GetSessionsByUserAccess(userID string) ([]*models.Session, error) {
	// Check if user has global access.
	user, err := d.GetUserByID(userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, fmt.Errorf("db: user %q not found", userID)
	}

	var rows *sql.Rows
	if user.HasGlobalAccess {
		rows, err = d.sql.Query(
			`SELECT s.session_id, s.contact_phone, s.status
			 FROM sessions s
			 LEFT JOIN (
			   SELECT session_id, MAX(timestamp) AS last_at
			   FROM messages GROUP BY session_id
			 ) m ON m.session_id = s.session_id
			 ORDER BY m.last_at DESC NULLS LAST`,
		)
	} else {
		rows, err = d.sql.Query(
			`SELECT s.session_id, s.contact_phone, s.status
			 FROM sessions s
			 JOIN contacts c ON c.contact_phone = s.contact_phone
			 LEFT JOIN (
			   SELECT session_id, MAX(timestamp) AS last_at
			   FROM messages GROUP BY session_id
			 ) m ON m.session_id = s.session_id
			 WHERE c.group_id IN (
			   SELECT group_id FROM user_groups WHERE user_id = ?
			 )
			 ORDER BY m.last_at DESC NULLS LAST`,
			userID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("db: GetSessionsByUserAccess: %w", err)
	}
	defer rows.Close()
	return scanSessions(rows)
}

// GetSessionByID fetches a single session by its ID.
func (d *DB) GetSessionByID(sessionID string) (*models.Session, error) {
	row := d.sql.QueryRow(
		`SELECT session_id, contact_phone, status FROM sessions WHERE session_id = ?`,
		sessionID,
	)
	var s models.Session
	if err := row.Scan(&s.SessionID, &s.ContactPhone, &s.Status); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("db: GetSessionByID: %w", err)
	}
	return &s, nil
}

// GetOpenSessionByPhone returns the open session for a contact, or nil if none.
func (d *DB) GetOpenSessionByPhone(contactPhone string) (*models.Session, error) {
	row := d.sql.QueryRow(
		`SELECT session_id, contact_phone, status FROM sessions
		 WHERE contact_phone = ? AND status = 'OPEN'
		 LIMIT 1`,
		contactPhone,
	)
	var s models.Session
	if err := row.Scan(&s.SessionID, &s.ContactPhone, &s.Status); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("db: GetOpenSessionByPhone: %w", err)
	}
	return &s, nil
}

// CreateSession creates a new OPEN session for the given contact phone.
func (d *DB) CreateSession(contactPhone string) (*models.Session, error) {
	s := &models.Session{
		SessionID:    newID(),
		ContactPhone: contactPhone,
		Status:       "OPEN",
	}
	_, err := d.sql.Exec(
		`INSERT INTO sessions (session_id, contact_phone, status) VALUES (?, ?, ?)`,
		s.SessionID, s.ContactPhone, s.Status,
	)
	if err != nil {
		return nil, fmt.Errorf("db: CreateSession: %w", err)
	}
	return s, nil
}

func scanSessions(rows *sql.Rows) ([]*models.Session, error) {
	var sessions []*models.Session
	for rows.Next() {
		var s models.Session
		if err := rows.Scan(&s.SessionID, &s.ContactPhone, &s.Status); err != nil {
			return nil, fmt.Errorf("db: scan session: %w", err)
		}
		sessions = append(sessions, &s)
	}
	return sessions, rows.Err()
}

// -----------------------------------------------------------------------------
// Messages
// -----------------------------------------------------------------------------

// GetMessagesBySession returns all messages for a session ordered by timestamp.
func (d *DB) GetMessagesBySession(sessionID string) ([]*models.Message, error) {
	rows, err := d.sql.Query(
		`SELECT message_id, session_id, direction, text, sent_by_user_id, timestamp
		 FROM messages WHERE session_id = ? ORDER BY timestamp ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("db: GetMessagesBySession: %w", err)
	}
	defer rows.Close()

	var msgs []*models.Message
	for rows.Next() {
		var m models.Message
		var ts string
		if err := rows.Scan(&m.MessageID, &m.SessionID, &m.Direction, &m.Text, &m.SentByUserID, &ts); err != nil {
			return nil, fmt.Errorf("db: scan message: %w", err)
		}
		m.Timestamp, _ = time.Parse(time.RFC3339, ts)
		msgs = append(msgs, &m)
	}
	return msgs, rows.Err()
}

// CreateMessage inserts a new message record. sentByUserID may be empty for inbound messages.
// gatewayMessageID is the SMS Gate message ID used to deduplicate webhook retries; pass empty for outbound.
// timestamp is the authoritative message time (e.g. receivedAt from SMS Gate for inbound, time.Now() for outbound).
// Returns (nil, nil) if the message was already recorded (duplicate).
func (d *DB) CreateMessage(sessionID, direction, text, sentByUserID, gatewayMessageID string, timestamp time.Time) (*models.Message, error) {
	m := &models.Message{
		MessageID: newID(),
		SessionID: sessionID,
		Direction: direction,
		Text:      text,
		Timestamp: timestamp,
	}
	if sentByUserID != "" {
		m.SentByUserID = sql.NullString{String: sentByUserID, Valid: true}
	}

	result, err := d.sql.Exec(
		`INSERT OR IGNORE INTO messages (message_id, session_id, direction, text, sent_by_user_id, timestamp, gateway_message_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		m.MessageID, m.SessionID, m.Direction, m.Text,
		nullableString(sentByUserID),
		m.Timestamp.Format("2006-01-02 15:04:05"),
		nullableString(gatewayMessageID),
	)
	if err != nil {
		return nil, fmt.Errorf("db: CreateMessage: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, nil // duplicate — already processed
	}
	return m, nil
}

// -----------------------------------------------------------------------------
// Contacts
// -----------------------------------------------------------------------------

// GetContactByPhone fetches a contact by their phone number.
func (d *DB) GetContactByPhone(phone string) (*models.Contact, error) {
	row := d.sql.QueryRow(
		`SELECT contact_phone, name, group_id, assigned_sim FROM contacts WHERE contact_phone = ?`,
		phone,
	)
	var c models.Contact
	if err := row.Scan(&c.ContactPhone, &c.Name, &c.GroupID, &c.AssignedSim); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("db: GetContactByPhone: %w", err)
	}
	return &c, nil
}

// CreateContact inserts a new contact with the given phone number and assigned SIM.
// Name and group are left NULL (unknown contact).
func (d *DB) CreateContact(phone string, assignedSim int) (*models.Contact, error) {
	c := &models.Contact{
		ContactPhone: phone,
		AssignedSim:  assignedSim,
	}
	_, err := d.sql.Exec(
		`INSERT INTO contacts (contact_phone, name, group_id, assigned_sim) VALUES (?, NULL, NULL, ?)`,
		phone, assignedSim,
	)
	if err != nil {
		return nil, fmt.Errorf("db: CreateContact: %w", err)
	}
	return c, nil
}

// RenameContact sets or updates a contact's name.
func (d *DB) RenameContact(phone, name, userID string) error {
	_, err := d.sql.Exec(
		`UPDATE contacts SET name = ? WHERE contact_phone = ?`,
		name, phone,
	)
	if err != nil {
		return fmt.Errorf("db: RenameContact: %w", err)
	}
	return nil
}

// AssignContact sets or updates a contact's group.
func (d *DB) AssignContact(phone, groupID, userID string) error {
	_, err := d.sql.Exec(
		`UPDATE contacts SET group_id = ? WHERE contact_phone = ?`,
		groupID, phone,
	)
	if err != nil {
		return fmt.Errorf("db: AssignContact: %w", err)
	}
	return nil
}

// RetireContact marks the old contact's phone as invalid by renaming it to
// phone+"-old", closes their open session, then creates a new unknown contact
// and session for the same number. Returns the new session.
func (d *DB) RetireContact(phone, userID string) (*models.Session, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, fmt.Errorf("db: RetireContact begin tx: %w", err)
	}
	defer tx.Rollback()

	oldPhone := phone + "-old"

	// Close any open session for the original phone.
	if _, err := tx.Exec(
		`UPDATE sessions SET status = 'CLOSED' WHERE contact_phone = ? AND status = 'OPEN'`,
		phone,
	); err != nil {
		return nil, fmt.Errorf("db: RetireContact close session: %w", err)
	}

	// Update all sessions to reference the old phone key.
	if _, err := tx.Exec(
		`UPDATE sessions SET contact_phone = ? WHERE contact_phone = ?`,
		oldPhone, phone,
	); err != nil {
		return nil, fmt.Errorf("db: RetireContact update sessions: %w", err)
	}

	// Rename the contact record.
	if _, err := tx.Exec(
		`UPDATE contacts SET contact_phone = ? WHERE contact_phone = ?`,
		oldPhone, phone,
	); err != nil {
		return nil, fmt.Errorf("db: RetireContact rename contact: %w", err)
	}

	// Create new unknown contact for the same phone number.
	if _, err := tx.Exec(
		`INSERT INTO contacts (contact_phone, name, group_id, assigned_sim) VALUES (?, NULL, NULL, 0)`,
		phone,
	); err != nil {
		return nil, fmt.Errorf("db: RetireContact create new contact: %w", err)
	}

	// Create a new open session for the new contact.
	newSession := &models.Session{
		SessionID:    newID(),
		ContactPhone: phone,
		Status:       "OPEN",
	}
	if _, err := tx.Exec(
		`INSERT INTO sessions (session_id, contact_phone, status) VALUES (?, ?, 'OPEN')`,
		newSession.SessionID, newSession.ContactPhone,
	); err != nil {
		return nil, fmt.Errorf("db: RetireContact create new session: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("db: RetireContact commit: %w", err)
	}

	return newSession, nil
}

// ErrContactExists is returned when trying to create a contact whose phone number is already in use.
var ErrContactExists = fmt.Errorf("contact already exists")

// ListGroups returns all groups ordered by name.
func (d *DB) ListGroups() ([]*models.Group, error) {
	rows, err := d.sql.Query(
		`SELECT group_id, group_name FROM groups ORDER BY group_name`,
	)
	if err != nil {
		return nil, fmt.Errorf("db: ListGroups: %w", err)
	}
	defer rows.Close()
	var groups []*models.Group
	for rows.Next() {
		var g models.Group
		if err := rows.Scan(&g.GroupID, &g.GroupName); err != nil {
			return nil, fmt.Errorf("db: scan group: %w", err)
		}
		groups = append(groups, &g)
	}
	return groups, rows.Err()
}

// ListGroupsForUser returns groups the user has access to.
// HR (global access) receives all groups; regular users receive only groups in their user_groups mapping.
func (d *DB) ListGroupsForUser(userID string) ([]*models.Group, error) {
	user, err := d.GetUserByID(userID)
	if err != nil {
		return nil, err
	}
	if user != nil && user.HasGlobalAccess {
		return d.ListGroups()
	}
	rows, err := d.sql.Query(
		`SELECT g.group_id, g.group_name FROM groups g
		 JOIN user_groups ug ON ug.group_id = g.group_id
		 WHERE ug.user_id = ?
		 ORDER BY g.group_name`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("db: ListGroupsForUser: %w", err)
	}
	defer rows.Close()
	var groups []*models.Group
	for rows.Next() {
		var g models.Group
		if err := rows.Scan(&g.GroupID, &g.GroupName); err != nil {
			return nil, fmt.Errorf("db: ListGroupsForUser scan: %w", err)
		}
		groups = append(groups, &g)
	}
	return groups, rows.Err()
}

// StartOutboundSession creates a new contact and an OPEN session for an outbound conversation.
// Returns ErrContactExists if the phone number is already in the contacts table.
func (d *DB) StartOutboundSession(phone, groupID, name string) (*models.Session, error) {
	existing, err := d.GetContactByPhone(phone)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, ErrContactExists
	}

	var nameArg interface{}
	if name != "" {
		nameArg = name
	}
	if _, err = d.sql.Exec(
		`INSERT INTO contacts (contact_phone, name, group_id, assigned_sim) VALUES (?, ?, ?, 0)`,
		phone, nameArg, groupID,
	); err != nil {
		return nil, fmt.Errorf("db: StartOutboundSession insert contact: %w", err)
	}

	sess, err := d.CreateSession(phone)
	if err != nil {
		return nil, fmt.Errorf("db: StartOutboundSession create session: %w", err)
	}
	return sess, nil
}

// GetUsersWithAccessToGroup returns user IDs of all users who can see a given group.
// If groupID is empty, returns only global-access users (HR).
func (d *DB) GetUsersWithAccessToGroup(groupID string) ([]string, error) {
	var userIDs []string

	// Always include global-access users.
	rows, err := d.sql.Query(
		`SELECT user_id FROM users WHERE has_global_access = 1`,
	)
	if err != nil {
		return nil, fmt.Errorf("db: GetUsersWithAccessToGroup global: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		userIDs = append(userIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if groupID == "" {
		return userIDs, nil
	}

	// Also include users explicitly mapped to this group.
	rows2, err := d.sql.Query(
		`SELECT user_id FROM user_groups WHERE group_id = ?`,
		groupID,
	)
	if err != nil {
		return nil, fmt.Errorf("db: GetUsersWithAccessToGroup group: %w", err)
	}
	defer rows2.Close()

	seen := make(map[string]struct{})
	for _, id := range userIDs {
		seen[id] = struct{}{}
	}
	for rows2.Next() {
		var id string
		if err := rows2.Scan(&id); err != nil {
			return nil, err
		}
		if _, ok := seen[id]; !ok {
			userIDs = append(userIDs, id)
			seen[id] = struct{}{}
		}
	}
	return userIDs, rows2.Err()
}

// -----------------------------------------------------------------------------
// Admin
// -----------------------------------------------------------------------------

// ListUsersWithGroups returns all users with their assigned group IDs.
func (d *DB) ListUsersWithGroups() ([]*models.UserWithGroups, error) {
	rows, err := d.sql.Query(
		`SELECT user_id, username, has_global_access FROM users ORDER BY username`,
	)
	if err != nil {
		return nil, fmt.Errorf("db: ListUsersWithGroups: %w", err)
	}
	defer rows.Close()

	var users []*models.UserWithGroups
	for rows.Next() {
		var u models.UserWithGroups
		var ga int
		if err := rows.Scan(&u.UserID, &u.Username, &ga); err != nil {
			return nil, fmt.Errorf("db: ListUsersWithGroups scan: %w", err)
		}
		u.HasGlobalAccess = ga != 0
		users = append(users, &u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, u := range users {
		gRows, err := d.sql.Query(
			`SELECT group_id FROM user_groups WHERE user_id = ? ORDER BY group_id`,
			u.UserID,
		)
		if err != nil {
			return nil, fmt.Errorf("db: ListUsersWithGroups groups: %w", err)
		}
		for gRows.Next() {
			var gid string
			if err := gRows.Scan(&gid); err != nil {
				gRows.Close()
				return nil, err
			}
			u.GroupIDs = append(u.GroupIDs, gid)
		}
		gRows.Close()
		if err := gRows.Err(); err != nil {
			return nil, err
		}
	}

	return users, nil
}

// CreateGroup inserts a new group and returns it.
func (d *DB) CreateGroup(name string) (*models.Group, error) {
	g := &models.Group{GroupID: newID(), GroupName: name}
	_, err := d.sql.Exec(
		`INSERT INTO groups (group_id, group_name) VALUES (?, ?)`,
		g.GroupID, g.GroupName,
	)
	if err != nil {
		return nil, fmt.Errorf("db: CreateGroup: %w", err)
	}
	return g, nil
}

// RenameGroup updates a group's name.
func (d *DB) RenameGroup(groupID, name string) error {
	_, err := d.sql.Exec(
		`UPDATE groups SET group_name = ? WHERE group_id = ?`,
		name, groupID,
	)
	if err != nil {
		return fmt.Errorf("db: RenameGroup: %w", err)
	}
	return nil
}

// DeleteGroup deletes a group. Returns an error if any contacts are still assigned to it.
// User-group mappings for the group are removed atomically in the same transaction.
func (d *DB) DeleteGroup(groupID string) error {
	var count int
	if err := d.sql.QueryRow(
		`SELECT COUNT(*) FROM contacts WHERE group_id = ?`, groupID,
	).Scan(&count); err != nil {
		return fmt.Errorf("db: DeleteGroup contact check: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("cannot delete group: %d contact(s) still assigned", count)
	}

	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("db: DeleteGroup begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM user_groups WHERE group_id = ?`, groupID); err != nil {
		return fmt.Errorf("db: DeleteGroup remove user_groups: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM groups WHERE group_id = ?`, groupID); err != nil {
		return fmt.Errorf("db: DeleteGroup delete: %w", err)
	}
	return tx.Commit()
}

// DeleteUser removes a user and their group mappings.
func (d *DB) DeleteUser(userID string) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("db: DeleteUser begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM user_groups WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("db: DeleteUser remove user_groups: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM users WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("db: DeleteUser delete: %w", err)
	}
	return tx.Commit()
}

// SetUserPermissions atomically replaces a user's has_global_access flag and group memberships.
func (d *DB) SetUserPermissions(userID string, groupIDs []string, hasGlobalAccess bool) error {
	ga := 0
	if hasGlobalAccess {
		ga = 1
	}

	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("db: SetUserPermissions begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`UPDATE users SET has_global_access = ? WHERE user_id = ?`, ga, userID,
	); err != nil {
		return fmt.Errorf("db: SetUserPermissions update flag: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM user_groups WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("db: SetUserPermissions clear groups: %w", err)
	}
	for _, gid := range groupIDs {
		if _, err := tx.Exec(
			`INSERT INTO user_groups (user_id, group_id) VALUES (?, ?)`, userID, gid,
		); err != nil {
			return fmt.Errorf("db: SetUserPermissions insert group %s: %w", gid, err)
		}
	}
	return tx.Commit()
}

// nullableString converts an empty string to a SQL NULL.
func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
