package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nerve-labs/nerve/internal/authz"
)

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrAlreadyInitialized = errors.New("this NERVE installation is already initialized")
	ErrInvalidSession     = errors.New("session is invalid or expired")
	ErrInvalidEmail       = errors.New("a valid email address is required")
)

type User struct {
	ID          string
	Email       string
	Role        string
	Permissions authz.Set
}

// Can reports whether the user holds a permission through any assigned role.
func (u User) Can(permission authz.Permission) bool {
	return u.Permissions.Has(permission)
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// loadPermissions returns the union of permissions granted by all roles of a user.
func loadPermissions(ctx context.Context, db rowQuerier, userID string) (authz.Set, error) {
	var keys []string
	err := db.QueryRow(ctx, `SELECT COALESCE(array_agg(DISTINCT rp.permission_key ORDER BY rp.permission_key), '{}')
		FROM user_roles ur JOIN role_permissions rp ON rp.role_id = ur.role_id
		WHERE ur.user_id = $1::uuid`, userID).Scan(&keys)
	if err != nil {
		return nil, fmt.Errorf("load permissions: %w", err)
	}
	return authz.NewSet(keys...), nil
}

type Service struct {
	db  *pgxpool.Pool
	ttl time.Duration
}

func NewService(db *pgxpool.Pool, ttl time.Duration) *Service {
	return &Service{db: db, ttl: ttl}
}

func (s *Service) Bootstrap(ctx context.Context, email, password, remoteAddress string) (User, string, time.Time, error) {
	email = normalizeEmail(email)
	if email == "" {
		return User{}, "", time.Time{}, ErrInvalidEmail
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, "", time.Time{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return User{}, "", time.Time{}, fmt.Errorf("begin administrator setup: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(716362944218312)`); err != nil {
		return User{}, "", time.Time{}, fmt.Errorf("lock administrator setup: %w", err)
	}

	var userCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&userCount); err != nil {
		return User{}, "", time.Time{}, fmt.Errorf("check installation state: %w", err)
	}
	if userCount != 0 {
		return User{}, "", time.Time{}, ErrAlreadyInitialized
	}

	user := User{Email: email, Role: "administrator"}
	if err := tx.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id::text`, email, hash).Scan(&user.ID); err != nil {
		return User{}, "", time.Time{}, fmt.Errorf("create administrator: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) SELECT $1::uuid, id FROM roles WHERE key = 'administrator'`, user.ID); err != nil {
		return User{}, "", time.Time{}, fmt.Errorf("assign administrator role: %w", err)
	}
	token, expiresAt, err := createSession(ctx, tx, user.ID, s.ttl)
	if err != nil {
		return User{}, "", time.Time{}, err
	}
	if user.Permissions, err = loadPermissions(ctx, tx, user.ID); err != nil {
		return User{}, "", time.Time{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log (actor_id, action, target_type, target_id, result, source_address) VALUES ($1::uuid, 'auth.bootstrap', 'user', $2, 'success', $3::inet)`, user.ID, user.ID, remoteAddress); err != nil {
		return User{}, "", time.Time{}, fmt.Errorf("audit administrator setup: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, "", time.Time{}, fmt.Errorf("commit administrator setup: %w", err)
	}
	return user, token, expiresAt, nil
}

func (s *Service) Login(ctx context.Context, email, password, remoteAddress string) (User, string, time.Time, error) {
	email = normalizeEmail(email)
	var user User
	var passwordHash string
	err := s.db.QueryRow(ctx, `SELECT u.id::text, u.email, u.password_hash, r.key
		FROM users u
		JOIN user_roles ur ON ur.user_id = u.id
		JOIN roles r ON r.id = ur.role_id
		WHERE u.email = $1 AND u.disabled_at IS NULL
		ORDER BY CASE r.key WHEN 'administrator' THEN 1 WHEN 'technician' THEN 2 ELSE 3 END
		LIMIT 1`, email).Scan(&user.ID, &user.Email, &passwordHash, &user.Role)
	if err != nil || !VerifyPassword(passwordHash, password) {
		return User{}, "", time.Time{}, ErrInvalidCredentials
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return User{}, "", time.Time{}, fmt.Errorf("begin login: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	token, expiresAt, err := createSession(ctx, tx, user.ID, s.ttl)
	if err != nil {
		return User{}, "", time.Time{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET last_login_at = now() WHERE id = $1`, user.ID); err != nil {
		return User{}, "", time.Time{}, fmt.Errorf("update last login: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log (actor_id, action, target_type, target_id, result, source_address) VALUES ($1::uuid, 'auth.login', 'session', $2, 'success', $3::inet)`, user.ID, user.ID, remoteAddress); err != nil {
		return User{}, "", time.Time{}, fmt.Errorf("audit login: %w", err)
	}
	if user.Permissions, err = loadPermissions(ctx, tx, user.ID); err != nil {
		return User{}, "", time.Time{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, "", time.Time{}, fmt.Errorf("commit login: %w", err)
	}
	return user, token, expiresAt, nil
}

func (s *Service) Session(ctx context.Context, token string) (User, error) {
	if token == "" {
		return User{}, ErrInvalidSession
	}
	digest := sha256.Sum256([]byte(token))
	var user User
	err := s.db.QueryRow(ctx, `SELECT u.id::text, u.email, r.key
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		JOIN user_roles ur ON ur.user_id = u.id
		JOIN roles r ON r.id = ur.role_id
		WHERE s.token_hash = $1 AND s.expires_at > now() AND u.disabled_at IS NULL
		ORDER BY CASE r.key WHEN 'administrator' THEN 1 WHEN 'technician' THEN 2 ELSE 3 END
		LIMIT 1`, digest[:]).Scan(&user.ID, &user.Email, &user.Role)
	if err != nil {
		return User{}, ErrInvalidSession
	}
	if user.Permissions, err = loadPermissions(ctx, s.db, user.ID); err != nil {
		return User{}, err
	}
	_, _ = s.db.Exec(ctx, `UPDATE sessions SET last_seen_at = now() WHERE token_hash = $1`, digest[:])
	return user, nil
}

func (s *Service) Logout(ctx context.Context, token string, user User, remoteAddress string) error {
	digest := sha256.Sum256([]byte(token))
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, digest[:]); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log (actor_id, action, target_type, target_id, result, source_address) VALUES ($1::uuid, 'auth.logout', 'session', $2, 'success', $3::inet)`, user.ID, user.ID, remoteAddress); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func createSession(ctx context.Context, tx pgx.Tx, userID string, ttl time.Duration) (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(token))
	expiresAt := time.Now().UTC().Add(ttl)
	if _, err := tx.Exec(ctx, `INSERT INTO sessions (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`, userID, digest[:], expiresAt); err != nil {
		return "", time.Time{}, fmt.Errorf("create session: %w", err)
	}
	return token, expiresAt, nil
}

func normalizeEmail(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	if !strings.Contains(email, "@") || len(email) > 320 {
		return ""
	}
	return email
}
