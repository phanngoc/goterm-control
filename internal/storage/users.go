package storage

import (
	"database/sql"
	"fmt"
	"time"
)

// User is the dashboard login account. BomClaw is single-account: there is
// exactly one row in the users table, created by `bomclaw passwd`.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	CreatedAt    time.Time
}

// UserStore manages the dashboard account and its web sessions.
type UserStore struct {
	db *DB
}

func NewUserStore(db *DB) *UserStore {
	return &UserStore{db: db}
}

// CreateUser inserts the account. Fails if the username exists.
func (s *UserStore) CreateUser(username, passwordHash string) error {
	_, err := s.db.conn.Exec(
		`INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)`,
		username, passwordHash, time.Now().Format(time.RFC3339),
	)
	return err
}

// GetUser returns a user by username, or nil if not found.
func (s *UserStore) GetUser(username string) (*User, error) {
	row := s.db.conn.QueryRow(
		`SELECT id, username, password_hash, created_at FROM users WHERE username = ?`, username)
	return scanUser(row)
}

// GetUserByID returns a user by ID, or nil if not found.
func (s *UserStore) GetUserByID(id int64) (*User, error) {
	row := s.db.conn.QueryRow(
		`SELECT id, username, password_hash, created_at FROM users WHERE id = ?`, id)
	return scanUser(row)
}

// FirstUser returns the existing account, or nil when none has been created.
// Used to enforce the single-account invariant in `bomclaw passwd`.
func (s *UserStore) FirstUser() (*User, error) {
	row := s.db.conn.QueryRow(
		`SELECT id, username, password_hash, created_at FROM users ORDER BY id LIMIT 1`)
	return scanUser(row)
}

func scanUser(row *sql.Row) (*User, error) {
	var u User
	var created string
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &created)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return &u, nil
}

// SetPassword updates the account's password hash.
func (s *UserStore) SetPassword(username, passwordHash string) error {
	res, err := s.db.conn.Exec(
		`UPDATE users SET password_hash = ? WHERE username = ?`, passwordHash, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("user %q not found", username)
	}
	return nil
}

// --- web sessions ---

// CreateWebSession stores a login session keyed by token hash.
func (s *UserStore) CreateWebSession(tokenHash string, userID int64, expiresAt time.Time) error {
	_, err := s.db.conn.Exec(
		`INSERT INTO web_sessions (token_hash, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		tokenHash, userID, expiresAt.Format(time.RFC3339), time.Now().Format(time.RFC3339),
	)
	return err
}

// GetWebSession resolves a token hash to its user, or nil if missing/expired.
// Expired rows are deleted opportunistically.
func (s *UserStore) GetWebSession(tokenHash string) (*User, error) {
	row := s.db.conn.QueryRow(
		`SELECT user_id, expires_at FROM web_sessions WHERE token_hash = ?`, tokenHash)
	var userID int64
	var expiresStr string
	err := row.Scan(&userID, &expiresStr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	expires, _ := time.Parse(time.RFC3339, expiresStr)
	if time.Now().After(expires) {
		_, _ = s.db.conn.Exec(`DELETE FROM web_sessions WHERE token_hash = ?`, tokenHash)
		return nil, nil
	}
	return s.GetUserByID(userID)
}

// DeleteWebSession removes one session (logout).
func (s *UserStore) DeleteWebSession(tokenHash string) error {
	_, err := s.db.conn.Exec(`DELETE FROM web_sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// DeleteAllWebSessions logs out every browser. Called on password change so a
// rotated password actually revokes access.
func (s *UserStore) DeleteAllWebSessions() error {
	_, err := s.db.conn.Exec(`DELETE FROM web_sessions`)
	return err
}
