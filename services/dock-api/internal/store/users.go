package store

import (
	"context"
	"strings"
	"time"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
)

// User is a demo account.
type User struct {
	UserID       int64      `json:"userId"`
	Email        string     `json:"email"`
	DisplayName  string     `json:"displayName"`
	PasswordHash string     `json:"-"`
	LastLoginAt  *time.Time `json:"lastLoginAt,omitempty"`
}

// UpsertUser creates the account or replaces its name and password hash.
func (s *Store) UpsertUser(ctx context.Context, email, name, passwordHash string) error {
	return errors.RepositoryError(s.GetDB(ctx).Exec(`
		INSERT INTO app_users (email, display_name, password_hash) VALUES (?, ?, ?)
		ON CONFLICT (email) DO UPDATE SET display_name = EXCLUDED.display_name,
		    password_hash = EXCLUDED.password_hash, updated_at = now()`,
		strings.ToLower(strings.TrimSpace(email)), name, passwordHash).Error)
}

// UserByEmail returns the account, or RECORD_NOT_FOUND.
func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	var users []User
	if err := s.GetDB(ctx).Raw(`SELECT user_id, email, display_name, password_hash, last_login_at FROM app_users
		WHERE email = ?`, strings.ToLower(strings.TrimSpace(email))).Scan(&users).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	if len(users) == 0 {
		return nil, errors.NewRecordNotFoundError("no account")
	}
	return &users[0], nil
}

// TouchLogin records a successful login.
func (s *Store) TouchLogin(ctx context.Context, userID int64) error {
	return errors.RepositoryError(s.GetDB(ctx).Exec(
		"UPDATE app_users SET last_login_at = now() WHERE user_id = ?", userID).Error)
}
