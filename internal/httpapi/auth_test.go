package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/magic-peach/NASA-APOD-Explorer/internal/store"
)

// sessionCookie returns the session cookie set by resp, failing if absent.
func sessionCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			return c
		}
	}
	t.Fatalf("no %s cookie in response (status %d)", sessionCookieName, resp.StatusCode)
	return nil
}

// signup creates a user and returns their session cookie.
func signup(t *testing.T, ts testEnv, username string) *http.Cookie {
	t.Helper()
	resp := do(t, http.MethodPost, ts.URL+"/api/auth/signup", nil,
		`{"username":"`+username+`","email":"`+username+`@example.com","password":"correct-horse"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("signup status %d", resp.StatusCode)
	}
	return sessionCookie(t, resp)
}

func TestSignupSetsHardenedSessionCookie(t *testing.T) {
	ts := newTestServer(t, &fakeAPOD{})
	c := signup(t, ts, "ada")

	if !c.HttpOnly {
		t.Error("cookie must be HttpOnly so page scripts (and XSS) can't read it")
	}
	if !c.Secure {
		t.Error("cookie must be Secure so it never travels over plain HTTP")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", c.SameSite)
	}
	if c.Path != "/" || c.MaxAge <= 0 {
		t.Errorf("Path=%q MaxAge=%d", c.Path, c.MaxAge)
	}

	resp := do(t, http.MethodGet, ts.URL+"/api/auth/me", c, "")
	var me userResponse
	_ = json.NewDecoder(resp.Body).Decode(&me)
	if resp.StatusCode != 200 || me.Username != "ada" {
		t.Fatalf("/me = %d %+v", resp.StatusCode, me)
	}
}

func TestSignupValidation(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{"valid", `{"username":"ada","email":"ada@example.com","password":"12345678"}`, 201},
		{"username too short", `{"username":"ab","email":"ab@example.com","password":"12345678"}`, 400},
		{"username with spaces", `{"username":"a b c","email":"x@example.com","password":"12345678"}`, 400},
		{"bad email", `{"username":"bob","email":"not-an-email","password":"12345678"}`, 400},
		{"password too short", `{"username":"bob","email":"bob@example.com","password":"1234567"}`, 400},
		{"password over bcrypt's 72 bytes", `{"username":"bob","email":"bob@example.com","password":"` + strings.Repeat("x", 73) + `"}`, 400},
		{"unknown field", `{"username":"bob","email":"bob@example.com","password":"12345678","admin":true}`, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := newTestServer(t, &fakeAPOD{})
			if resp := do(t, http.MethodPost, ts.URL+"/api/auth/signup", nil, tt.body); resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
		})
	}
}

func TestDuplicateSignupConflicts(t *testing.T) {
	ts := newTestServer(t, &fakeAPOD{})
	signup(t, ts, "ada")
	resp := do(t, http.MethodPost, ts.URL+"/api/auth/signup", nil,
		`{"username":"ada","email":"other@example.com","password":"correct-horse"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", resp.StatusCode)
	}
}

func TestLoginFailuresLookIdentical(t *testing.T) {
	ts := newTestServer(t, &fakeAPOD{})
	signup(t, ts, "ada")

	read := func(body string) (int, string) {
		resp := do(t, http.MethodPost, ts.URL+"/api/auth/login", nil, body)
		var e struct{ Error apiError }
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return resp.StatusCode, e.Error.Message
	}
	s1, m1 := read(`{"username":"ada","password":"wrong-password"}`)
	s2, m2 := read(`{"username":"nobody","password":"wrong-password"}`)
	if s1 != 401 || s2 != 401 || m1 != m2 {
		t.Errorf("wrong password: %d %q; unknown user: %d %q; must be identical 401s", s1, m1, s2, m2)
	}

	// Email works as the login name too.
	if resp := do(t, http.MethodPost, ts.URL+"/api/auth/login", nil,
		`{"username":"ada@example.com","password":"correct-horse"}`); resp.StatusCode != 200 {
		t.Errorf("login by email: status %d", resp.StatusCode)
	}
}

func TestLogoutKillsSessionServerSide(t *testing.T) {
	ts := newTestServer(t, &fakeAPOD{})
	c := signup(t, ts, "ada")

	resp := do(t, http.MethodPost, ts.URL+"/api/auth/logout", c, "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout status %d", resp.StatusCode)
	}
	// Even a copy of the old cookie must now be useless.
	if resp := do(t, http.MethodGet, ts.URL+"/api/auth/me", c, ""); resp.StatusCode != 401 {
		t.Errorf("old cookie after logout: status %d, want 401", resp.StatusCode)
	}
}

func TestSessionsExpire(t *testing.T) {
	ts := newTestServer(t, &fakeAPOD{})
	c := signup(t, ts, "ada")

	ts.clock.Advance(59 * time.Minute) // test TTL is one hour
	if resp := do(t, http.MethodGet, ts.URL+"/api/auth/me", c, ""); resp.StatusCode != 200 {
		t.Fatalf("before expiry: %d", resp.StatusCode)
	}
	ts.clock.Advance(2 * time.Minute)
	if resp := do(t, http.MethodGet, ts.URL+"/api/auth/me", c, ""); resp.StatusCode != 401 {
		t.Errorf("after expiry: status %d, want 401", resp.StatusCode)
	}
}

func TestSessionTokenIsNotStoredInPlaintext(t *testing.T) {
	ts := newTestServer(t, &fakeAPOD{})
	c := signup(t, ts, "ada")

	_, err := ts.db.SessionUser(context.Background(), c.Value, ts.clock.Now())
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("raw cookie value found in the sessions table (err=%v); only its HMAC should be stored", err)
	}
}

func TestLegacyPasswordHashIsUpgradedOnLogin(t *testing.T) {
	ts := newTestServer(t, &fakeAPOD{})
	ctx := context.Background()

	// A user created by the old app: hex(SHA-256(salt + ":" + password)),
	// with the default salt since the test server doesn't set one.
	sum := sha256.Sum256([]byte(":" + "old-password"))
	if _, err := ts.db.CreateUser(ctx, "legacy", "legacy@example.com", hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}

	resp := do(t, http.MethodPost, ts.URL+"/api/auth/login", nil, `{"username":"legacy","password":"old-password"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("legacy login: status %d", resp.StatusCode)
	}
	u, err := ts.db.UserByLogin(ctx, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u.PasswordHash, "$2") {
		t.Errorf("hash not upgraded to bcrypt: %q", u.PasswordHash)
	}
	// And the upgraded hash still accepts the same password.
	if resp := do(t, http.MethodPost, ts.URL+"/api/auth/login", nil, `{"username":"legacy","password":"old-password"}`); resp.StatusCode != 200 {
		t.Errorf("login after upgrade: status %d", resp.StatusCode)
	}
}

func TestFavoritesCRUD(t *testing.T) {
	ts := newTestServer(t, &fakeAPOD{})
	ada := signup(t, ts, "ada")
	bob := signup(t, ts, "bob")
	favs := ts.URL + "/api/favorites"

	list := func(c *http.Cookie) []store.Favorite {
		t.Helper()
		resp := do(t, http.MethodGet, favs, c, "")
		var out []store.Favorite
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}

	if resp := do(t, http.MethodPost, favs, nil, `{"date":"2024-01-01"}`); resp.StatusCode != 401 {
		t.Fatalf("anonymous add: %d, want 401", resp.StatusCode)
	}

	// Saving the same day twice must not create a duplicate.
	for range 2 {
		if resp := do(t, http.MethodPost, favs, ada, `{"date":"2024-01-01","title":"Moon","url":"u"}`); resp.StatusCode != 200 {
			t.Fatalf("add: %d", resp.StatusCode)
		}
	}
	if got := list(ada); len(got) != 1 || got[0].Title != "Moon" {
		t.Fatalf("ada's favorites = %+v, want exactly one", got)
	}
	if got := list(bob); len(got) != 0 {
		t.Fatalf("bob sees ada's favorites: %+v", got)
	}

	if resp := do(t, http.MethodDelete, favs+"/2024-01-01", bob, ""); resp.StatusCode != 404 {
		t.Errorf("bob deleting ada's favorite: %d, want 404", resp.StatusCode)
	}
	if resp := do(t, http.MethodDelete, favs+"/2024-01-01", ada, ""); resp.StatusCode != 204 {
		t.Errorf("delete: %d, want 204", resp.StatusCode)
	}
	if resp := do(t, http.MethodDelete, favs+"/2024-01-01", ada, ""); resp.StatusCode != 404 {
		t.Errorf("second delete: %d, want 404", resp.StatusCode)
	}
	if resp := do(t, http.MethodDelete, favs+"/not-a-date", ada, ""); resp.StatusCode != 400 {
		t.Errorf("bad date: %d, want 400", resp.StatusCode)
	}
	if got := list(ada); len(got) != 0 {
		t.Errorf("favorites after delete = %+v", got)
	}
}

func TestCrossSiteWritesAreBlocked(t *testing.T) {
	ts := newTestServer(t, &fakeAPOD{})
	c := signup(t, ts, "ada")

	send := func(origin string) int {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/favorites", strings.NewReader(`{"date":"2024-01-01"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		req.AddCookie(c)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := send("https://evil.example"); code != http.StatusForbidden {
		t.Errorf("cross-site POST: %d, want 403", code)
	}
	if code := send(ts.URL); code != 200 {
		t.Errorf("same-origin POST: %d, want 200", code)
	}
}

func TestHistoryRecordsOnlyExplicitViews(t *testing.T) {
	ts := newTestServer(t, &fakeAPOD{})
	c := signup(t, ts, "ada")

	// Fetching entries (e.g. for a gallery) is not "viewing" them.
	do(t, http.MethodGet, ts.URL+"/api/apod?count=3", c, "")

	list := func() []store.HistoryEntry {
		resp := do(t, http.MethodGet, ts.URL+"/api/history", c, "")
		var out []store.HistoryEntry
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("history after a gallery fetch = %+v, want empty", got)
	}

	if resp := do(t, http.MethodPost, ts.URL+"/api/history", c, `{"date":"2024-01-01","title":"Moon"}`); resp.StatusCode != 204 {
		t.Fatalf("record view: %d", resp.StatusCode)
	}
	if got := list(); len(got) != 1 || got[0].Title != "Moon" {
		t.Fatalf("history = %+v, want the one viewed entry", got)
	}
	if resp := do(t, http.MethodPost, ts.URL+"/api/history", nil, `{"date":"2024-01-01"}`); resp.StatusCode != 401 {
		t.Errorf("anonymous record view: %d, want 401", resp.StatusCode)
	}
	if resp := do(t, http.MethodPost, ts.URL+"/api/history", c, `{"date":"2099-01-01"}`); resp.StatusCode != 400 {
		t.Errorf("future date: %d, want 400", resp.StatusCode)
	}
}
