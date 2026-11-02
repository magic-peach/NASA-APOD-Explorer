package store

import (
	"context"
	"errors"
	"testing"

	"github.com/magic-peach/NASA-APOD-Explorer/internal/apod"
)

// newTestStore returns a fresh in-memory database, closed when the test ends.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), ":memory:")
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
	in := apod.Item{Date: "2024-01-01", Title: "Moon", URL: "u", MediaType: "image"}
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
