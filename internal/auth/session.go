package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// DefaultSessionExpiry is the default session duration (24 hours).
const DefaultSessionExpiry = 24 * time.Hour

// Session represents a user session stored in the database.
type Session struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// SessionManager handles session creation, validation, and cleanup.
type SessionManager struct {
	db     *sql.DB
	expiry time.Duration
}

// NewSessionManager creates a SessionManager with the given database and expiry duration.
// If expiry is zero, DefaultSessionExpiry is used.
func NewSessionManager(db *sql.DB, expiry time.Duration) *SessionManager {
	if expiry <= 0 {
		expiry = DefaultSessionExpiry
	}
	return &SessionManager{db: db, expiry: expiry}
}

// CreateSession creates a new session for the given user ID and stores it in the database.
func (sm *SessionManager) CreateSession(userID string) (*Session, error) {
	id, err := generateSessionID()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	expiresAt := now.Add(sm.expiry)

	_, err = sm.db.Exec(
		"INSERT INTO sessions (id, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)",
		id, userID, expiresAt.Format(time.RFC3339), now.Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}

	return &Session{
		ID:        id,
		UserID:    userID,
		ExpiresAt: expiresAt,
		CreatedAt: now,
	}, nil
}

// ValidateSession checks if a session exists and has not expired.
// Returns the session if valid, or an error if not found or expired.
func (sm *SessionManager) ValidateSession(sessionID string) (*Session, error) {
	var s Session
	var expiresAtStr, createdAtStr string

	err := sm.db.QueryRow(
		"SELECT id, user_id, expires_at, created_at FROM sessions WHERE id = ?",
		sessionID,
	).Scan(&s.ID, &s.UserID, &expiresAtStr, &createdAtStr)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("session not found")
	}
	if err != nil {
		return nil, fmt.Errorf("query session: %w", err)
	}

	expiresAt, err := time.Parse(time.RFC3339, expiresAtStr)
	if err != nil {
		// Try alternative format that SQLite might use
		expiresAt, err = time.Parse("2006-01-02T15:04:05Z", expiresAtStr)
		if err != nil {
			return nil, fmt.Errorf("parse expires_at: %w", err)
		}
	}
	s.ExpiresAt = expiresAt

	createdAt, err := time.Parse(time.RFC3339, createdAtStr)
	if err != nil {
		createdAt, err = time.Parse("2006-01-02T15:04:05Z", createdAtStr)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
	}
	s.CreatedAt = createdAt

	if time.Now().UTC().After(s.ExpiresAt) {
		return nil, fmt.Errorf("session expired")
	}

	// Absolute session timeout: sessions older than 7 days are always invalid
	// regardless of the sliding expiry window
	const maxSessionAge = 7 * 24 * time.Hour
	if time.Now().UTC().Sub(s.CreatedAt) > maxSessionAge {
		// Clean up the stale session
		sm.db.Exec("DELETE FROM sessions WHERE id = ?", sessionID)
		return nil, fmt.Errorf("session expired (max age)")
	}

	return &s, nil
}

// CleanExpired removes all expired sessions from the database.
// Returns the number of sessions removed.
func (sm *SessionManager) CleanExpired() (int64, error) {
	result, err := sm.db.Exec(
		"DELETE FROM sessions WHERE expires_at <= ?",
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return result.RowsAffected()
}

// DeleteSession removes a specific session by ID.
func (sm *SessionManager) DeleteSession(sessionID string) error {
	_, err := sm.db.Exec("DELETE FROM sessions WHERE id = ?", sessionID)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteSessionsByUserID removes all sessions for a given user ID.
// Used for session rotation on login and user cleanup.
func (sm *SessionManager) DeleteSessionsByUserID(userID string) error {
	_, err := sm.db.Exec("DELETE FROM sessions WHERE user_id = ?", userID)
	if err != nil {
		return fmt.Errorf("delete sessions by user ID: %w", err)
	}
	return nil
}

// VerifyAdminPassword checks if the provided password matches the stored bcrypt hash.
// Returns nil if the password is correct, or an error otherwise.
func VerifyAdminPassword(password, passwordHash string) error {
	if passwordHash == "" {
		return fmt.Errorf("admin password not configured")
	}
	err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password))
	if err != nil {
		return fmt.Errorf("密码错误")
	}
	return nil
}

// HashPassword generates a bcrypt hash for the given password.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// generateSessionID creates a cryptographically random hex string for session IDs.
// Uses 32 bytes (256 bits) of entropy for strong session security.
func generateSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}
