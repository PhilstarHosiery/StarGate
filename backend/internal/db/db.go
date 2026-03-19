package db

import (
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/PhilstarHosiery/stargate/backend/internal/models"
	_ "modernc.org/sqlite"
)

//go:embed migrations/001_initial.sql
var migrationSQL string


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

// Migrate runs the embedded migration SQL to create tables if they don't exist.
func Migrate(d *DB) error {
	if _, err := d.sql.Exec(migrationSQL); err != nil {
		return fmt.Errorf("db: migrate: %w", err)
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
			 ORDER BY s.session_id`,
		)
	} else {
		rows, err = d.sql.Query(
			`SELECT s.session_id, s.contact_phone, s.status
			 FROM sessions s
			 JOIN contacts c ON c.contact_phone = s.contact_phone
			 WHERE c.group_id IN (
			   SELECT group_id FROM user_groups WHERE user_id = ?
			 )
			 ORDER BY s.session_id`,
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
		m.Timestamp, _ = time.Parse("2006-01-02 15:04:05", ts)
		msgs = append(msgs, &m)
	}
	return msgs, rows.Err()
}

// CreateMessage inserts a new message record. sentByUserID may be empty for inbound messages.
func (d *DB) CreateMessage(sessionID, direction, text, sentByUserID string) (*models.Message, error) {
	m := &models.Message{
		MessageID: newID(),
		SessionID: sessionID,
		Direction: direction,
		Text:      text,
		Timestamp: time.Now().UTC(),
	}
	if sentByUserID != "" {
		m.SentByUserID = sql.NullString{String: sentByUserID, Valid: true}
	}

	_, err := d.sql.Exec(
		`INSERT INTO messages (message_id, session_id, direction, text, sent_by_user_id, timestamp)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		m.MessageID, m.SessionID, m.Direction, m.Text,
		nullableString(sentByUserID),
		m.Timestamp.Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return nil, fmt.Errorf("db: CreateMessage: %w", err)
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

// nullableString converts an empty string to a SQL NULL.
func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
