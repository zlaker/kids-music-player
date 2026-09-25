// Package auth keeps user accounts and login sessions in SQLite.
//
// The browser is not the user. After a correct password the server creates a
// random session id, stores only its hash, and sends the id back in an
// HttpOnly cookie. Every later request presents that cookie, and the server
// looks the session up to learn who is calling and whether the account is
// still active.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

const (
	StatusActive   = "active"
	StatusDisabled = "disabled"

	CookieName    = "session"
	sessionTTL    = 30 * 24 * time.Hour
	SessionMaxAge = 30 * 24 * 60 * 60
)

var (
	ErrCredentials = errors.New("неверное имя или пароль")
	ErrDisabled    = errors.New("учётная запись отключена")
)

// User is an account the middleware has accepted for this request.
type User struct {
	ID     int64
	Name   string
	Status string
}

// Store is the accounts database. It lives next to the player's JSON state.
type Store struct {
	db   *sql.DB
	cost int
}

// Open creates the database file and its tables.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open auth db: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, cost: bcrypt.DefaultCost}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// SetCostForTest lowers the password hash cost. Production stays on the
// bcrypt default, which is intentionally slow.
func (s *Store) SetCostForTest(cost int) {
	s.cost = cost
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS sessions (
			token_hash TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL REFERENCES users(id),
			expires_at TEXT NOT NULL
		);
	`)
	if err != nil {
		return fmt.Errorf("create auth tables: %w", err)
	}
	return nil
}

// Count reports how many accounts exist. Zero means the server stays open,
// which is the LAN default when nobody configured a password.
func (s *Store) Count() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// Seed ensures the account from the flags exists. An existing account keeps
// its status; a changed password in the environment replaces the hash.
func (s *Store) Seed(name, password string) error {
	name = strings.TrimSpace(name)
	if name == "" && password == "" {
		return nil
	}
	if name == "" || password == "" {
		return errors.New("auth seed needs both a name and a password")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.cost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	var id int64
	var current string
	err = s.db.QueryRow(`SELECT id, password_hash FROM users WHERE name = ?`, name).Scan(&id, &current)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = s.db.Exec(
			`INSERT INTO users (name, password_hash, status, created_at) VALUES (?, ?, ?, ?)`,
			name, string(hash), StatusActive, time.Now().UTC().Format(time.RFC3339),
		)
		if err != nil {
			return fmt.Errorf("seed user: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("lookup seed user: %w", err)
	}
	if bcrypt.CompareHashAndPassword([]byte(current), []byte(password)) == nil {
		return nil
	}
	_, err = s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, string(hash), id)
	if err != nil {
		return fmt.Errorf("update seed password: %w", err)
	}
	return nil
}

// SetStatus marks an account active or disabled. Disabled accounts keep their
// rows, but their sessions stop working.
func (s *Store) SetStatus(name, status string) error {
	if status != StatusActive && status != StatusDisabled {
		return fmt.Errorf("unknown status %q", status)
	}
	res, err := s.db.Exec(`UPDATE users SET status = ? WHERE name = ?`, status, name)
	if err != nil {
		return fmt.Errorf("set status: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set status: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("no such user %q", name)
	}
	return nil
}

// Login checks the password and returns a new session token. The token is the
// value that goes into the cookie; only its hash is written to disk.
func (s *Store) Login(name, password string) (string, error) {
	var id int64
	var hash, status string
	err := s.db.QueryRow(
		`SELECT id, password_hash, status FROM users WHERE name = ?`, strings.TrimSpace(name),
	).Scan(&id, &hash, &status)
	if errors.Is(err, sql.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return "", ErrCredentials
	}
	if err != nil {
		return "", fmt.Errorf("lookup user: %w", err)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return "", ErrCredentials
	}
	if status != StatusActive {
		return "", ErrDisabled
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`,
		tokenHash(token), id, time.Now().Add(sessionTTL).UTC().Format(time.RFC3339),
	)
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return token, nil
}

// UserByToken resolves a cookie value to an active account. An expired or
// disabled session is not a user.
func (s *Store) UserByToken(token string) (User, error) {
	if token == "" {
		return User{}, ErrCredentials
	}
	var u User
	var expires string
	err := s.db.QueryRow(`
		SELECT u.id, u.name, u.status, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ?`, tokenHash(token),
	).Scan(&u.ID, &u.Name, &u.Status, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrCredentials
	}
	if err != nil {
		return User{}, fmt.Errorf("lookup session: %w", err)
	}
	until, err := time.Parse(time.RFC3339, expires)
	if err != nil || time.Now().After(until) || u.Status != StatusActive {
		_ = s.Logout(token)
		if u.Status == StatusDisabled {
			return User{}, ErrDisabled
		}
		return User{}, ErrCredentials
	}
	return u, nil
}

// Logout forgets one session. A missing token is not an error.
func (s *Store) Logout(token string) error {
	if token == "" {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, tokenHash(token))
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// dummyHash makes a login for an unknown name take a password check too.
var dummyHash []byte

func init() {
	var err error
	dummyHash, err = bcrypt.GenerateFromPassword([]byte("not-a-user"), bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
}

func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("session token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
