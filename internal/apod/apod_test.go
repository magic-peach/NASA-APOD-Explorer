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
