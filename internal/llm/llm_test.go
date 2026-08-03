package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunFallsBackWithoutAPIKey(t *testing.T) {
	a := &Assistant{}
	got := a.Run(context.Background(), Request{Task: "eli10"})
	if got.Source != "fallback" || got.Text == "" {
		t.Errorf("got %+v, want non-empty fallback", got)
	}
}

func TestRunFallsBackOnUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	a := &Assistant{APIKey: "k", BaseURL: srv.URL, HTTPClient: srv.Client()}
	if got := a.Run(context.Background(), Request{Task: "quiz"}); got.Source != "fallback" {
		t.Errorf("source = %q, want fallback", got.Source)
	}
}

func TestRunUsesModelReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Error("missing bearer token")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"  hi there  "}}]}`))
	}))
	defer srv.Close()

	a := &Assistant{APIKey: "k", BaseURL: srv.URL, HTTPClient: srv.Client()}
	got := a.Run(context.Background(), Request{Task: "facts"})
	if got.Source != "llm" || got.Text != "hi there" {
		t.Errorf("got %+v", got)
	}
}

func TestFallbackTruncatesOnRuneBoundary(t *testing.T) {
	long := ""
	for range 300 {
		long += "é" // 2 bytes per rune: byte slicing would split one
	}
	out := fallback(Request{Task: "rewrite", Level: "Expert", Explanation: long})
	for _, r := range out {
		if r == '�' {
			t.Fatal("fallback produced invalid UTF-8")
		}
	}
}
