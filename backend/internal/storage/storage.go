package storage

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

var ErrNotFound = errors.New("note not found")

type Note struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Pinned    bool      `json:"pinned"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Input struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Pinned  bool   `json:"pinned"`
}

type List struct {
	Notes  []Note `json:"notes"`
	Total  int    `json:"total"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

type Store struct{ db *pgxpool.Pool }

func New(db *pgxpool.Pool) *Store { return &Store{db: db} }

func (s *Store) Ping(ctx context.Context) error { return s.db.Ping(ctx) }

func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	// Serialize migrations when several instances start at the same time.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(73482910)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		var applied bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)", entry.Name()).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		sql, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("migration %s: %w", entry.Name(), err)
		}
		if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES ($1)", entry.Name()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

const columns = "id, title, content, pinned, created_at, updated_at"

func scanNote(row pgx.Row) (Note, error) {
	var note Note
	err := row.Scan(&note.ID, &note.Title, &note.Content, &note.Pinned, &note.CreatedAt, &note.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return note, ErrNotFound
	}
	return note, err
}

func (s *Store) List(ctx context.Context, userID int64, query string, limit, offset int) (List, error) {
	result := List{Notes: []Note{}, Limit: limit, Offset: offset}
	// Keep total and rows consistent if notes change during this request.
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer tx.Rollback(context.Background())
	const filter = " WHERE user_id = $1 AND ($2 = '' OR strpos(lower(title), lower($2)) > 0 OR strpos(lower(content), lower($2)) > 0)"
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM notes"+filter, userID, query).Scan(&result.Total); err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, "SELECT "+columns+" FROM notes"+filter+" ORDER BY pinned DESC, updated_at DESC, id DESC LIMIT $3 OFFSET $4", userID, query, limit, offset)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		note, err := scanNote(rows)
		if err != nil {
			return result, err
		}
		result.Notes = append(result.Notes, note)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func (s *Store) Get(ctx context.Context, userID, id int64) (Note, error) {
	return scanNote(s.db.QueryRow(ctx, "SELECT "+columns+" FROM notes WHERE id = $1 AND user_id = $2", id, userID))
}

func (s *Store) Create(ctx context.Context, userID int64, input Input) (Note, error) {
	return scanNote(s.db.QueryRow(ctx, "INSERT INTO notes(user_id, title, content, pinned) VALUES ($1, $2, $3, $4) RETURNING "+columns, userID, input.Title, input.Content, input.Pinned))
}

func (s *Store) Update(ctx context.Context, userID, id int64, input Input) (Note, error) {
	return scanNote(s.db.QueryRow(ctx, "UPDATE notes SET title = $2, content = $3, pinned = $4, updated_at = NOW() WHERE id = $1 AND user_id = $5 RETURNING "+columns, id, input.Title, input.Content, input.Pinned, userID))
}

func (s *Store) Delete(ctx context.Context, userID, id int64) error {
	tag, err := s.db.Exec(ctx, "DELETE FROM notes WHERE id = $1 AND user_id = $2", id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
