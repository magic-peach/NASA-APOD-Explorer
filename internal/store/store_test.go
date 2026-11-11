package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/magic-peach/NASA-APOD-Explorer/internal/apod"
)

// newTestStore returns a fresh in-memory database, closed when the test ends.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), ":memory:", "")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCreateUserRejectsDuplicates(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.CreateUser(ctx, "ada", "ada@example.com", "h"); err != nil {
		t.Fatalf("first signup: %v", err)
	}
	if _, err := s.CreateUser(ctx, "ada", "other@example.com", "h"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate username: got %v, want ErrConflict", err)
	}
	if _, err := s.UserByLogin(ctx, "ada@example.com"); err != nil {
		t.Fatalf("login by email: %v", err)
	}
	if _, err := s.UserByLogin(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: got %v, want ErrNotFound", err)
	}
}

func TestAddFavoriteIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	uid, _ := s.CreateUser(ctx, "ada", "ada@example.com", "h")

	for range 3 {
		if err := s.AddFavorite(ctx, uid, Favorite{Date: "2024-01-01", Title: "Moon"}); err != nil {
			t.Fatal(err)
		}
	}
	favs, err := s.ListFavorites(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(favs) != 1 {
		t.Fatalf("got %d favorites after saving the same date 3 times, want 1", len(favs))
	}
}

func TestSQLInjectionIsInert(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	evil := "x' OR '1'='1"
	if _, err := s.UserByLogin(ctx, evil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("injection string matched a user: %v", err)
	}
}

func TestAPODCacheRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	in := apod.Item{Date: "2024-01-01", Title: "Moon", URL: "u", MediaType: "video",
		Copyright: "Jane Doe", ThumbnailURL: "thumb.jpg"}
	if err := s.SaveAPOD(ctx, []apod.Item{in}); err != nil {
		t.Fatal(err)
	}
	got, err := s.CachedAPOD(ctx, "2024-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if got != in {
		t.Errorf("got %+v, want %+v", got, in)
	}
}

func TestRatingsAverage(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	a, _ := s.CreateUser(ctx, "a", "a@x", "h")
	b, _ := s.CreateUser(ctx, "b", "b@x", "h")
	_ = s.SetRating(ctx, a, "2024-01-01", 5)
	_ = s.SetRating(ctx, b, "2024-01-01", 2)
	_ = s.SetRating(ctx, b, "2024-01-01", 4) // re-rating replaces, not adds

	avg, err := s.AverageRating(ctx, "2024-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if avg != 4.5 {
		t.Errorf("average = %v, want 4.5", avg)
	}
}

func TestMigrationsAreIdempotentAndVersioned(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	v, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v != 3 {
		t.Fatalf("schema version = %d, want 3", v)
	}
	// Running migrate again (as every restart does) must be a no-op.
	if err := s.migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if v2, _ := s.SchemaVersion(ctx); v2 != v {
		t.Fatalf("version changed from %d to %d on re-run", v, v2)
	}
}

func TestSessionsExpire(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	uid, _ := s.CreateUser(ctx, "ada", "ada@x", "h")
	now := time.Unix(1_700_000_000, 0)

	if err := s.CreateSession(ctx, "hash1", uid, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SessionUser(ctx, "hash1", now.Add(59*time.Minute)); err != nil || got != uid {
		t.Fatalf("live session: got (%d, %v)", got, err)
	}
	if _, err := s.SessionUser(ctx, "hash1", now.Add(time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session: got %v, want ErrNotFound", err)
	}
	if n, _ := s.DeleteExpiredSessions(ctx, now.Add(2*time.Hour)); n != 1 {
		t.Fatalf("purged %d sessions, want 1", n)
	}
}

func TestDeleteFavoriteOnlyAffectsOwner(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	alice, _ := s.CreateUser(ctx, "alice", "a@x", "h")
	bob, _ := s.CreateUser(ctx, "bob", "b@x", "h")
	_ = s.AddFavorite(ctx, alice, Favorite{Date: "2024-01-01"})

	if err := s.DeleteFavorite(ctx, bob, "2024-01-01"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob deleting alice's favorite: got %v, want ErrNotFound", err)
	}
	if err := s.DeleteFavorite(ctx, alice, "2024-01-01"); err != nil {
		t.Fatalf("alice deleting her favorite: %v", err)
	}
	if favs, _ := s.ListFavorites(ctx, alice); len(favs) != 0 {
		t.Fatalf("favorite still present: %+v", favs)
	}
}

func TestPlaceholdersAndSamplesAreNeverPersisted(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	err := s.SaveAPOD(ctx, []apod.Item{
		{Date: "2024-01-01", Title: "NASA Science", Placeholder: true},
		{Date: "2024-01-02", Title: "Sample", Sample: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"2024-01-01", "2024-01-02"} {
		if _, err := s.CachedAPOD(ctx, d); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s was persisted (err=%v); it would mask the real entry forever", d, err)
		}
	}
}

func TestIsRemote(t *testing.T) {
	cases := map[string]bool{
		"app.db":                          false,
		"/data/app.db":                    false,
		":memory:":                        false,
		"libsql://apod-explorer.turso.io": true,
		"https://apod-explorer.turso.io":  true,
		"http://127.0.0.1:8080":           true,
	}
	for path, want := range cases {
		if got := IsRemote(path); got != want {
			t.Errorf("IsRemote(%q) = %v, want %v", path, got, want)
		}
	}
}
