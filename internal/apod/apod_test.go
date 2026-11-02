package apod

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQueryKeyIsStableAndExcludesAPIKey(t *testing.T) {
	a := Query{StartDate: "2024-01-01", EndDate: "2024-01-07"}
	b := Query{EndDate: "2024-01-07", StartDate: "2024-01-01"}
	if a.Key() != b.Key() {
		t.Fatalf("equal queries gave different keys: %q vs %q", a.Key(), b.Key())
	}
	if want := "end_date=2024-01-07&start_date=2024-01-01"; a.Key() != want {
		t.Errorf("Key() = %q, want %q", a.Key(), want)
	}
}

func TestHTTPClientFetch(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		status    int
		wantItems int
		wantErr   bool
	}{
		{name: "single object", status: 200, body: `{"date":"2024-01-01","title":"A"}`, wantItems: 1},
		{name: "array", status: 200, body: `[{"date":"2024-01-01"},{"date":"2024-01-02"}]`, wantItems: 2},
		{name: "upstream error", status: 500, body: `oops`, wantErr: true},
		{name: "malformed json", status: 200, body: `{`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("api_key") != "secret" {
					t.Errorf("api_key not sent")
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c := &HTTPClient{BaseURL: srv.URL, APIKey: "secret", HTTPClient: srv.Client()}
			items, err := c.Fetch(context.Background(), Query{Date: "2024-01-01"})
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(items) != tt.wantItems {
				t.Errorf("got %d items, want %d", len(items), tt.wantItems)
			}
		})
	}
}

// TestTransportErrorsDoNotLeakAPIKey guards against the API key reaching
// logs: net/http's *url.Error embeds the request URL (with ?api_key=) in
// its message.
func TestTransportErrorsDoNotLeakAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // nothing listening: every request fails at the transport level

	c := &HTTPClient{BaseURL: srv.URL, APIKey: "super-secret-key", HTTPClient: http.DefaultClient}
	_, err := c.Fetch(context.Background(), Query{Date: "2024-01-01"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "super-secret-key") {
		t.Fatalf("error message leaks the API key: %v", err)
	}
}

// TestIsSingleMatchesQueryPrecedence checks IsSingle agrees with values():
// whichever form values() sends upstream decides the response shape.
func TestIsSingleMatchesQueryPrecedence(t *testing.T) {
	tests := []struct {
		name string
		q    Query
		want bool
	}{
		{"today", Query{}, true},
		{"date", Query{Date: "2024-01-01"}, true},
		{"range", Query{StartDate: "2024-01-01", EndDate: "2024-01-02"}, false},
		{"count", Query{Count: 3}, false},
		{"date wins over count", Query{Date: "2024-01-01", Count: 3}, true},
		{"range wins over date", Query{Date: "2024-01-01", StartDate: "2024-01-01", EndDate: "2024-01-02"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.q.IsSingle(); got != tt.want {
				t.Errorf("IsSingle() = %v, want %v (query sent upstream: %q)", got, tt.want, tt.q.Key())
			}
		})
	}
}

func TestIsPlaceholder(t *testing.T) {
	tests := []struct {
		name string
		it   Item
		want bool
	}{
		{"boilerplate explanation", Item{Title: "x", Explanation: "Discover the cosmos! Each day a different image..."}, true},
		{"generic title with logo image", Item{Title: "NASA Science", URL: "https://science.nasa.gov/wp-content/themes/nasa-child/assets/images/nasa-logo.svg"}, true},
		{"real entry", Item{Title: "Stereo Helene", Explanation: "Saturn's moon Helene...", URL: "https://apod.nasa.gov/apod/image/2406/helene.jpg"}, false},
		{"real entry that mentions NASA Science", Item{Title: "NASA Science at Work", URL: "https://apod.nasa.gov/x.jpg"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsPlaceholder(tt.it); got != tt.want {
				t.Errorf("IsPlaceholder = %v, want %v", got, tt.want)
			}
		})
	}
}
