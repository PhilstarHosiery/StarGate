package models

import (
	"database/sql"
	"time"
)

// User represents a JavaFX operator who logs into the system.
type User struct {
	UserID          string
	Username        string
	PasswordHash    string
	HasGlobalAccess bool
}

// Contact represents anyone who texts the system (employee, supplier, unknown).
// Identified by phone number; name and group can be assigned later.
type Contact struct {
	ContactPhone string
	Name         sql.NullString
	GroupID      sql.NullString
	AssignedSim  int
}

// Group represents a category that Contacts belong to (e.g. "Welding").
type Group struct {
	GroupID   string
	GroupName string
}

// Session represents an ongoing conversation thread with a Contact.
type Session struct {
	SessionID    string
	ContactPhone string
	Status       string // "OPEN" | "CLOSED"
}

// Message represents a single text sent or received within a Session.
type Message struct {
	MessageID     string
	SessionID     string
	Direction     string // "INBOUND" | "OUTBOUND"
	Text          string
	SentByUserID  sql.NullString
	Timestamp     time.Time
}
