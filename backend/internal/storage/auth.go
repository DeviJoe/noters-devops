package storage

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrUsernameTaken   = errors.New("username already exists")
	ErrUserNotFound    = errors.New("user not found")
	ErrUnauthenticated = errors.New("session missing or expired")
)

type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

func (s *Store) CreateUser(ctx context.Context, username, hash string) (User, error) {
	var user User
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return user, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(73482911)"); err != nil {
		return user, err
	}
	var first bool
	if err = tx.QueryRow(ctx, "SELECT NOT EXISTS(SELECT 1 FROM users)").Scan(&first); err != nil {
		return user, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO users(username, password_hash) VALUES ($1, $2)
		RETURNING id, username, created_at`, username, hash).Scan(&user.ID, &user.Username, &user.CreatedAt)
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			return user, ErrUsernameTaken
		}
		return user, err
	}
	if first {
		if _, err = tx.Exec(ctx, "UPDATE notes SET user_id = $1 WHERE user_id IS NULL", user.ID); err != nil {
			return user, err
		}
		if _, err = tx.Exec(ctx, "ALTER TABLE notes VALIDATE CONSTRAINT notes_user_required"); err != nil {
			return user, err
		}
	}
	return user, tx.Commit(ctx)
}

func (s *Store) UserByUsername(ctx context.Context, username string) (User, error) {
	var user User
	err := s.db.QueryRow(ctx, "SELECT id, username, password_hash, created_at FROM users WHERE username = $1", username).
		Scan(&user.ID, &user.Username, &user.PasswordHash, &user.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return user, ErrUserNotFound
	}
	return user, err
}

func (s *Store) CreateSession(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "DELETE FROM sessions WHERE expires_at <= NOW()"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO sessions(token_hash, user_id, expires_at) VALUES ($1, $2, $3)", tokenHash, userID, expiresAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) SessionUser(ctx context.Context, tokenHash string) (User, error) {
	var user User
	err := s.db.QueryRow(ctx, `SELECT u.id, u.username, u.created_at FROM users u
		JOIN sessions s ON s.user_id = u.id WHERE s.token_hash = $1 AND s.expires_at > NOW()`, tokenHash).
		Scan(&user.ID, &user.Username, &user.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return user, ErrUnauthenticated
	}
	return user, err
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.Exec(ctx, "DELETE FROM sessions WHERE token_hash = $1", tokenHash)
	return err
}
