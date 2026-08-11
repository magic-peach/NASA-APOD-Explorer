// Package store persists users and their activity in SQLite.
//
// It uses database/sql with modernc.org/sqlite, a pure-Go SQLite driver, so
// the binary needs no CGO and no sqlite3 executable at runtime. Every query
// uses ? placeholders: user input is passed as a parameter and never spliced
// into SQL text, which rules out SQL injection.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/magic-peach/NASA-APOD-Explorer/internal/apod"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Sentinel errors let handlers map storage outcomes to HTTP status codes
// without depending on driver-specific error types.
var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: already exists")
)

// Store wraps the database handle.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and applies
// any pending migrations. Use ":memory:" for a throwaway database in tests.
func Open(ctx context.Context, path string) (*Store, error) {
	// _pragma parameters are applied by the driver to every new connection:
	//   foreign_keys  - enforce REFERENCES constraints (off by default in SQLite)
	//   busy_timeout  - wait up to 5s for a lock instead of failing immediately
	//   journal_mode  - WAL lets readers proceed while a write is in progress
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	if path != ":memory:" {
		dsn += "&_pragma=journal_mode(WAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	if path == ":memory:" {
		// Each new connection to ":memory:" would get its own empty
		// database, so pin the pool to a single connection.
		db.SetMaxOpenConns(1)
	}

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Ping verifies the database is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// ---- users ---------------------------------------------------------------

// User is the subset of a user row needed for login.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
}

// CreateUser inserts a new user. It returns ErrConflict if the username or
// email is already taken.
func (s *Store) CreateUser(ctx context.Context, username, email, passwordHash string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, email, password_hash) VALUES (?, ?, ?)`,
		username, email, passwordHash)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrConflict
		}
		return 0, fmt.Errorf("store: create user: %w", err)
	}
	return res.LastInsertId()
}

// UserByLogin looks a user up by username or email.
func (s *Store) UserByLogin(ctx context.Context, login string) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash FROM users WHERE username = ? OR email = ? LIMIT 1`,
		login, login).Scan(&u.ID, &u.Username, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("store: user by login: %w", err)
	}
	return u, nil
}

// UserByID returns the user with the given ID.
func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Username, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("store: user by id: %w", err)
	}
	return u, nil
}

// UpdatePasswordHash replaces a user's stored password hash (used to
// upgrade legacy hashes to bcrypt on login).
func (s *Store) UpdatePasswordHash(ctx context.Context, userID int64, hash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, userID)
	if err != nil {
		return fmt.Errorf("store: update password hash: %w", err)
	}
	return nil
}

// ---- sessions ------------------------------------------------------------

// CreateSession stores a session. tokenHash must be a keyed hash of the
// session token, never the token itself.
func (s *Store) CreateSession(ctx context.Context, tokenHash string, userID int64, now, expires time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		tokenHash, userID, now.Unix(), expires.Unix())
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

// SessionUser returns the user ID for an unexpired session, or ErrNotFound.
func (s *Store) SessionUser(ctx context.Context, tokenHash string, now time.Time) (int64, error) {
	var userID int64
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id FROM sessions WHERE token_hash = ? AND expires_at > ?`,
		tokenHash, now.Unix()).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("store: session user: %w", err)
	}
	return userID, nil
}

// DeleteSession removes one session (logout). Deleting a missing session
// is not an error.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash); err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return nil
}

// DeleteExpiredSessions purges sessions past their expiry and reports how
// many it removed.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.Unix())
	if err != nil {
		return 0, fmt.Errorf("store: delete expired sessions: %w", err)
	}
	return res.RowsAffected()
}

// ---- favorites -----------------------------------------------------------

// Favorite is an APOD entry a user has saved.
type Favorite struct {
	ID      int64     `json:"id"`
	Date    string    `json:"date"`
	URL     string    `json:"url"`
	Title   string    `json:"title"`
	SavedAt time.Time `json:"saved_at"`
}

// AddFavorite saves (or refreshes) a favorite. The UNIQUE(user_id,
// apod_date) constraint plus ON CONFLICT makes this an upsert, so saving
// the same date twice never creates a duplicate row.
func (s *Store) AddFavorite(ctx context.Context, userID int64, f Favorite) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO favorites (user_id, apod_date, image_url, title) VALUES (?, ?, ?, ?)
			ON CONFLICT (user_id, apod_date) DO UPDATE SET
				image_url = excluded.image_url,
				title = excluded.title,
				saved_at = CURRENT_TIMESTAMP`,
			userID, f.Date, f.URL, f.Title); err != nil {
			return fmt.Errorf("store: add favorite: %w", err)
		}
		return bumpStat(ctx, tx, f.Date, "likes")
	})
}

// DeleteFavorite removes one of a user's favorites. It returns ErrNotFound
// if the user had no favorite for that date. The user_id condition means a
// user can only ever delete their own favorites.
func (s *Store) DeleteFavorite(ctx context.Context, userID int64, date string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM favorites WHERE user_id = ? AND apod_date = ?`, userID, date)
	if err != nil {
		return fmt.Errorf("store: delete favorite: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListFavorites returns a user's favorites, newest first.
func (s *Store) ListFavorites(ctx context.Context, userID int64) ([]Favorite, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, apod_date, COALESCE(image_url, ''), COALESCE(title, ''), saved_at
		FROM favorites WHERE user_id = ? ORDER BY saved_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list favorites: %w", err)
	}
	defer rows.Close()

	favs := []Favorite{} // non-nil so it encodes as [] rather than null
	for rows.Next() {
		var f Favorite
		if err := rows.Scan(&f.ID, &f.Date, &f.URL, &f.Title, &f.SavedAt); err != nil {
			return nil, fmt.Errorf("store: scan favorite: %w", err)
		}
		favs = append(favs, f)
	}
	return favs, rows.Err()
}

// ---- history -------------------------------------------------------------

// HistoryEntry records one APOD a user viewed.
type HistoryEntry struct {
	Date     string    `json:"date"`
	Title    string    `json:"title"`
	ViewedAt time.Time `json:"viewed_at"`
}

// AddHistory records views for a batch of entries in one transaction.
func (s *Store) AddHistory(ctx context.Context, userID int64, items []apod.Item) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, it := range items {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO history (user_id, apod_date, title) VALUES (?, ?, ?)`,
				userID, it.Date, it.Title); err != nil {
				return fmt.Errorf("store: add history: %w", err)
			}
			if err := bumpStat(ctx, tx, it.Date, "views"); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListHistory returns a user's 30 most recent views.
func (s *Store) ListHistory(ctx context.Context, userID int64) ([]HistoryEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT apod_date, COALESCE(title, ''), viewed_at
		FROM history WHERE user_id = ? ORDER BY viewed_at DESC, id DESC LIMIT 30`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list history: %w", err)
	}
	defer rows.Close()

	entries := []HistoryEntry{}
	for rows.Next() {
		var h HistoryEntry
		if err := rows.Scan(&h.Date, &h.Title, &h.ViewedAt); err != nil {
			return nil, fmt.Errorf("store: scan history: %w", err)
		}
		entries = append(entries, h)
	}
	return entries, rows.Err()
}

// ---- comments ------------------------------------------------------------

// Comment is one user comment on an APOD date.
type Comment struct {
	Username  string    `json:"username"`
	Comment   string    `json:"comment"`
	CreatedAt time.Time `json:"created_at"`
}

// AddComment stores a comment and bumps the date's comment counter.
func (s *Store) AddComment(ctx context.Context, userID int64, date, text string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO comments (user_id, image_date, comment_text) VALUES (?, ?, ?)`,
			userID, date, text); err != nil {
			return fmt.Errorf("store: add comment: %w", err)
		}
		return bumpStat(ctx, tx, date, "comments")
	})
}

// ListComments returns the comments on a date, newest first.
func (s *Store) ListComments(ctx context.Context, date string) ([]Comment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT u.username, COALESCE(c.comment_text, ''), c.created_at
		FROM comments c JOIN users u ON u.id = c.user_id
		WHERE c.image_date = ? ORDER BY c.created_at DESC, c.id DESC`, date)
	if err != nil {
		return nil, fmt.Errorf("store: list comments: %w", err)
	}
	defer rows.Close()

	comments := []Comment{}
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.Username, &c.Comment, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan comment: %w", err)
		}
		comments = append(comments, c)
	}
	return comments, rows.Err()
}

// ---- ratings -------------------------------------------------------------

// SetRating records a user's 1–5 rating for a date, replacing any earlier one.
func (s *Store) SetRating(ctx context.Context, userID int64, date string, rating int) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ratings (user_id, image_date, rating) VALUES (?, ?, ?)
		ON CONFLICT (user_id, image_date) DO UPDATE SET rating = excluded.rating`,
		userID, date, rating)
	if err != nil {
		return fmt.Errorf("store: set rating: %w", err)
	}
	return nil
}

// AverageRating returns the mean rating for a date rounded to 2 decimals,
// or 0 when nobody has rated it.
func (s *Store) AverageRating(ctx context.Context, date string) (float64, error) {
	var avg float64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(AVG(rating), 0) FROM ratings WHERE image_date = ?`, date).Scan(&avg)
	if err != nil {
		return 0, fmt.Errorf("store: average rating: %w", err)
	}
	return math.Round(avg*100) / 100, nil
}

// ---- persistent APOD cache -----------------------------------------------

// CachedAPOD returns a previously stored APOD entry for date.
func (s *Store) CachedAPOD(ctx context.Context, date string) (apod.Item, error) {
	var it apod.Item
	err := s.db.QueryRowContext(ctx, `
		SELECT date, COALESCE(title, ''), COALESCE(explanation, ''), COALESCE(image_url, ''),
		       COALESCE(media_type, ''), COALESCE(hd_url, ''), COALESCE(copyright, ''),
		       COALESCE(thumbnail_url, '')
		FROM apod_cache WHERE date = ?`, date).
		Scan(&it.Date, &it.Title, &it.Explanation, &it.URL, &it.MediaType, &it.HDURL,
			&it.Copyright, &it.ThumbnailURL)
	if errors.Is(err, sql.ErrNoRows) {
		return apod.Item{}, ErrNotFound
	}
	if err != nil {
		return apod.Item{}, fmt.Errorf("store: cached apod: %w", err)
	}
	return it, nil
}

// SaveAPOD upserts entries into the persistent APOD cache. A past date's
// entry never changes, so it is kept forever; that is exactly why NASA
// placeholders and demo samples are skipped: storing one would hide the
// real entry permanently.
func (s *Store) SaveAPOD(ctx context.Context, items []apod.Item) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, it := range items {
			if it.Date == "" || it.Placeholder || it.Sample {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO apod_cache (date, title, explanation, image_url, media_type, hd_url,
				                        copyright, thumbnail_url)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (date) DO UPDATE SET
					title = excluded.title, explanation = excluded.explanation,
					image_url = excluded.image_url, media_type = excluded.media_type,
					hd_url = excluded.hd_url, copyright = excluded.copyright,
					thumbnail_url = excluded.thumbnail_url, stored_at = CURRENT_TIMESTAMP`,
				it.Date, it.Title, it.Explanation, it.URL, it.MediaType, it.HDURL,
				it.Copyright, it.ThumbnailURL); err != nil {
				return fmt.Errorf("store: save apod: %w", err)
			}
		}
		return nil
	})
}

// ---- helpers -------------------------------------------------------------

// inTx runs fn inside a transaction, committing on success and rolling back
// on error, so multi-statement writes are all-or-nothing.
func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback() // the original error is more useful than a rollback error
		return err
	}
	return tx.Commit()
}

// bumpStatSQL holds one complete statement per counter. SQL placeholders
// can only stand in for values, not column names, so instead of building
// the SQL at runtime each column gets its own constant statement.
var bumpStatSQL = map[string]string{
	"views": `INSERT INTO image_stats (image_date, views) VALUES (?, 1)
		ON CONFLICT (image_date) DO UPDATE SET views = views + 1`,
	"likes": `INSERT INTO image_stats (image_date, likes) VALUES (?, 1)
		ON CONFLICT (image_date) DO UPDATE SET likes = likes + 1`,
	"comments": `INSERT INTO image_stats (image_date, comments) VALUES (?, 1)
		ON CONFLICT (image_date) DO UPDATE SET comments = comments + 1`,
}

// bumpStat increments one counter column of image_stats for date.
func bumpStat(ctx context.Context, tx *sql.Tx, date, column string) error {
	q, ok := bumpStatSQL[column]
	if !ok {
		return fmt.Errorf("store: unknown stat column %q", column)
	}
	if _, err := tx.ExecContext(ctx, q, date); err != nil {
		return fmt.Errorf("store: bump %s: %w", column, err)
	}
	return nil
}

// isUniqueViolation reports whether err is a SQLite UNIQUE constraint error.
// Matching on the message keeps us independent of the driver's error types.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
