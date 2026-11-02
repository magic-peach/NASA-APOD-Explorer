package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/magic-peach/NASA-APOD-Explorer/internal/apod"
	"github.com/magic-peach/NASA-APOD-Explorer/internal/llm"
	"github.com/magic-peach/NASA-APOD-Explorer/internal/spacefacts"
	"github.com/magic-peach/NASA-APOD-Explorer/internal/store"
)

// fakeAPOD satisfies APODClient without any network access. This is the
// payoff of handlers depending on an interface instead of *apod.HTTPClient.
type fakeAPOD struct {
	items []apod.Item
	err   error
	calls int
}

func (f *fakeAPOD) Fetch(_ context.Context, _ apod.Query) ([]apod.Item, error) {
	f.calls++
	return f.items, f.err
}

type fakeFacts struct{}

func (fakeFacts) Summary(context.Context) (spacefacts.Summary, error) {
	return spacefacts.Summary{Planets: 8}, nil
}

type fakeAssistant struct{}

func (fakeAssistant) Run(_ context.Context, req llm.Request) llm.Response {
	return llm.Response{Text: "answer to " + req.Task, Source: "fallback"}
}

// newTestServer wires a Server against fakes and an in-memory database.
func newTestServer(t *testing.T, api *fakeAPOD) *httptest.Server {
	t.Helper()
	db, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	srv := New(Deps{
		APOD:      api,
		Facts:     fakeFacts{},
		Assistant: fakeAssistant{},
		Store:     db,
		Static:    fstest.MapFS{"index.html": {Data: []byte("<h1>hello</h1>")}},
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestAPODResponseShapes(t *testing.T) {
	two := []apod.Item{{Date: "2024-01-01", Title: "A"}, {Date: "2024-01-02", Title: "B"}}

	tests := []struct {
		name      string
		query     string
		wantArray bool
	}{
		{name: "single date returns an object", query: "?date=2024-01-01", wantArray: false},
		{name: "range returns an array", query: "?start_date=2024-01-01&end_date=2024-01-02", wantArray: true},
		{name: "count returns an array", query: "?count=2", wantArray: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := newTestServer(t, &fakeAPOD{items: two})
			resp, err := http.Get(ts.URL + "/api/apod" + tt.query)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			body, _ := io.ReadAll(resp.Body)
			isArray := strings.HasPrefix(strings.TrimSpace(string(body)), "[")
			if isArray != tt.wantArray {
				t.Errorf("array=%v, want %v; body=%s", isArray, tt.wantArray, body)
			}
		})
	}
}

func TestAPODSingleDateServedFromDatabaseAfterFirstFetch(t *testing.T) {
	api := &fakeAPOD{items: []apod.Item{{Date: "2024-01-01", Title: "A"}}}
	ts := newTestServer(t, api)

	for range 3 {
		resp, err := http.Get(ts.URL + "/api/apod?date=2024-01-01")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if api.calls != 1 {
		t.Errorf("upstream called %d times, want 1 (later hits come from SQLite)", api.calls)
	}
}

func TestAPODUpstreamFailureIsBadGateway(t *testing.T) {
	ts := newTestServer(t, &fakeAPOD{err: errors.New("nasa down")})
	resp, err := http.Get(ts.URL + "/api/apod?date=2024-01-01")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestSignupLoginFavoritesFlow(t *testing.T) {
	ts := newTestServer(t, &fakeAPOD{})

	post := func(path, token, body string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	if r := post("/api/signup", "", `{"username":"ada","email":"ada@x","password":"secret1"}`); r.StatusCode != 200 {
		t.Fatalf("signup status %d", r.StatusCode)
	}
	if r := post("/api/favorites", "", `{"date":"2024-01-01"}`); r.StatusCode != 401 {
		t.Fatalf("favorites without login: status %d, want 401", r.StatusCode)
	}

	r := post("/api/login", "", `{"username":"ada","password":"secret1"}`)
	var login struct{ Token string }
	_ = json.NewDecoder(r.Body).Decode(&login)
	r.Body.Close()
	if login.Token == "" {
		t.Fatal("no token returned")
	}

	if r := post("/api/favorites", login.Token, `{"date":"2024-01-01","title":"Moon"}`); r.StatusCode != 200 {
		t.Fatalf("add favorite status %d", r.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/favorites", nil)
	req.Header.Set("Authorization", "Bearer "+login.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var favs []store.Favorite
	_ = json.NewDecoder(resp.Body).Decode(&favs)
	if len(favs) != 1 || favs[0].Title != "Moon" {
		t.Errorf("favorites = %+v", favs)
	}
}

func TestStaticFilesAreServed(t *testing.T) {
	ts := newTestServer(t, &fakeAPOD{})
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "hello") {
		t.Errorf("index not served: %s", body)
	}
}
