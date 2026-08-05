package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/magic-peach/NASA-APOD-Explorer/internal/apod"
	"github.com/magic-peach/NASA-APOD-Explorer/internal/llm"
	"github.com/magic-peach/NASA-APOD-Explorer/internal/spacefacts"
	"github.com/magic-peach/NASA-APOD-Explorer/internal/store"
)

// fakeAPOD satisfies APODClient without any network access. This is the
// payoff of handlers depending on an interface instead of *apod.HTTPClient.
// It is called concurrently by the on-this-day handler, hence the mutex.
type fakeAPOD struct {
	mu        sync.Mutex
	items     []apod.Item     // if set, returned for every query
	err       error           // if set, returned for every query
	failDates map[string]bool // dates that fail individually
	calls     int
}

func (f *fakeAPOD) Fetch(_ context.Context, q apod.Query) ([]apod.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.failDates[q.Date] {
		return nil, errors.New("upstream error for " + q.Date)
	}
	if f.items != nil {
		return f.items, nil
	}
	return []apod.Item{{Date: q.Date, Title: "Entry " + q.Date}}, nil
}

func (f *fakeAPOD) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeFacts struct{}

func (fakeFacts) Summary(context.Context) (spacefacts.Summary, error) {
	return spacefacts.Summary{Planets: 8}, nil
}

type fakeAssistant struct{}

func (fakeAssistant) Run(_ context.Context, req llm.Request) llm.Response {
	return llm.Response{Text: "answer to " + req.Task, Source: "fallback"}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fixedNow pins "today" so date validation is deterministic.
var fixedNow = time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

type testEnv struct {
	*httptest.Server
	db *store.Store
}

// newTestServer wires a Server against fakes and an in-memory database.
func newTestServer(t *testing.T, api *fakeAPOD) testEnv {
	t.Helper()
	db, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	srv := New(Deps{
		APOD:           api,
		Facts:          fakeFacts{},
		Assistant:      fakeAssistant{},
		Store:          db,
		Static:         fstest.MapFS{"index.html": {Data: []byte("<h1>hello</h1>")}},
		Logger:         discardLogger(),
		Now:            func() time.Time { return fixedNow },
		RateLimitRPS:   1000, // keep the limiter out of the way in functional tests
		RateLimitBurst: 1000,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return testEnv{Server: ts, db: db}
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func postJSON(t *testing.T, url, token, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
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
			resp, body := get(t, ts.URL+"/api/apod"+tt.query)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d: %s", resp.StatusCode, body)
			}
			isArray := strings.HasPrefix(strings.TrimSpace(body), "[")
			if isArray != tt.wantArray {
				t.Errorf("array=%v, want %v; body=%s", isArray, tt.wantArray, body)
			}
		})
	}
}

func TestAPODSingleDateServedFromDatabaseAfterFirstFetch(t *testing.T) {
	api := &fakeAPOD{}
	ts := newTestServer(t, api)
	for range 3 {
		get(t, ts.URL+"/api/apod?date=2024-01-01")
	}
	if n := api.callCount(); n != 1 {
		t.Errorf("upstream called %d times, want 1 (later hits come from SQLite)", n)
	}
}

func TestErrorsUseConsistentJSONShape(t *testing.T) {
	tests := []struct {
		name       string
		api        *fakeAPOD
		path       string
		wantStatus int
		wantCode   string
	}{
		{name: "invalid date", api: &fakeAPOD{}, path: "/api/apod?date=yesterday", wantStatus: 400, wantCode: codeInvalidDate},
		{name: "future date", api: &fakeAPOD{}, path: "/api/apod?date=2030-01-01", wantStatus: 400, wantCode: codeDateOutOfRange},
		{name: "count too big", api: &fakeAPOD{}, path: "/api/apod?count=50", wantStatus: 400, wantCode: codeInvalidCount},
		{name: "upstream down", api: &fakeAPOD{err: errors.New("down")}, path: "/api/apod?date=2024-01-01", wantStatus: 502, wantCode: codeUpstream},
		{name: "unknown endpoint", api: &fakeAPOD{}, path: "/api/nope", wantStatus: 404, wantCode: codeNotFound},
		{name: "not logged in", api: &fakeAPOD{}, path: "/api/favorites", wantStatus: 401, wantCode: codeUnauthorized},
		{name: "comments need a valid date", api: &fakeAPOD{}, path: "/api/comments?date=bad", wantStatus: 400, wantCode: codeInvalidDate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := newTestServer(t, tt.api)
			resp, body := get(t, ts.URL+tt.path)
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", resp.StatusCode, tt.wantStatus, body)
			}
			var parsed struct {
				Error apiError `json:"error"`
			}
			if err := json.Unmarshal([]byte(body), &parsed); err != nil || parsed.Error.Code != tt.wantCode || parsed.Error.Message == "" {
				t.Errorf("body = %s, want error code %q with a message", body, tt.wantCode)
			}
		})
	}
}

func TestPostRequiresJSONContentType(t *testing.T) {
	ts := newTestServer(t, &fakeAPOD{})
	resp, err := http.Post(ts.URL+"/api/signup", "application/x-www-form-urlencoded",
		strings.NewReader("username=a&email=b&password=secret1"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestOnThisDay(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		failDates map[string]bool
		wantDates []string
	}{
		{
			name:      "previous years in order",
			query:     "?date=2024-05-20&years=3",
			wantDates: []string{"2023-05-20", "2022-05-20", "2021-05-20"},
		},
		{
			name:      "29 February only exists in leap years",
			query:     "?date=2024-02-29&years=8",
			wantDates: []string{"2020-02-29", "2016-02-29"},
		},
		{
			name:      "nothing before APOD began",
			query:     "?date=1997-06-20&years=5",
			wantDates: []string{"1996-06-20", "1995-06-20"},
		},
		{
			name:      "one failing year is skipped, not fatal",
			query:     "?date=2024-05-20&years=3",
			failDates: map[string]bool{"2022-05-20": true},
			wantDates: []string{"2023-05-20", "2021-05-20"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := newTestServer(t, &fakeAPOD{failDates: tt.failDates})
			resp, body := get(t, ts.URL+"/api/apod/on-this-day"+tt.query)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d: %s", resp.StatusCode, body)
			}
			var items []apod.Item
			if err := json.Unmarshal([]byte(body), &items); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, it := range items {
				got = append(got, it.Date)
			}
			if strings.Join(got, ",") != strings.Join(tt.wantDates, ",") {
				t.Errorf("dates = %v, want %v", got, tt.wantDates)
			}
		})
	}
}

func TestOnThisDayAllFailingIsBadGateway(t *testing.T) {
	ts := newTestServer(t, &fakeAPOD{err: errors.New("down")})
	resp, _ := get(t, ts.URL+"/api/apod/on-this-day?date=2024-05-20&years=2")
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestHealthAndReadiness(t *testing.T) {
	ts := newTestServer(t, &fakeAPOD{})

	if resp, _ := get(t, ts.URL+"/healthz"); resp.StatusCode != 200 {
		t.Errorf("/healthz = %d", resp.StatusCode)
	}
	if resp, _ := get(t, ts.URL+"/readyz"); resp.StatusCode != 200 {
		t.Errorf("/readyz with a healthy DB = %d", resp.StatusCode)
	}

	ts.db.Close() // simulate the database going away
	if resp, _ := get(t, ts.URL+"/readyz"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("/readyz with a closed DB = %d, want 503", resp.StatusCode)
	}
	if resp, _ := get(t, ts.URL+"/healthz"); resp.StatusCode != 200 {
		t.Errorf("/healthz must not depend on the DB, got %d", resp.StatusCode)
	}
}

func TestResponsesCarryRequestID(t *testing.T) {
	ts := newTestServer(t, &fakeAPOD{})
	resp, _ := get(t, ts.URL+"/healthz")
	if resp.Header.Get("X-Request-ID") == "" {
		t.Error("missing X-Request-ID header")
	}
}

func TestSignupLoginFavoritesFlow(t *testing.T) {
	ts := newTestServer(t, &fakeAPOD{})

	if r := postJSON(t, ts.URL+"/api/signup", "", `{"username":"ada","email":"ada@x","password":"secret1"}`); r.StatusCode != 200 {
		t.Fatalf("signup status %d", r.StatusCode)
	}
	if r := postJSON(t, ts.URL+"/api/favorites", "", `{"date":"2024-01-01"}`); r.StatusCode != 401 {
		t.Fatalf("favorites without login: status %d, want 401", r.StatusCode)
	}

	r := postJSON(t, ts.URL+"/api/login", "", `{"username":"ada","password":"secret1"}`)
	var login struct{ Token string }
	_ = json.NewDecoder(r.Body).Decode(&login)
	if login.Token == "" {
		t.Fatal("no token returned")
	}

	if r := postJSON(t, ts.URL+"/api/favorites", login.Token, `{"date":"2024-01-01","title":"Moon"}`); r.StatusCode != 200 {
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
	_, body := get(t, ts.URL+"/")
	if !strings.Contains(body, "hello") {
		t.Errorf("index not served: %s", body)
	}
}
