package spacefacts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/magic-peach/NASA-APOD-Explorer/internal/cache"
)

func TestSummarise(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	moons := []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`)}

	got := summarise([]body{
		{BodyType: "Planet", Moons: moons, Gravity: f(9.8), MeanRadius: f(6371)},
		{BodyType: "planet", Gravity: f(3.7), MeanRadius: f(3389.5)}, // case-insensitive match
		{BodyType: "Moon"}, // no gravity/radius: excluded from averages
	})
	want := Summary{Planets: 2, Moons: 2, AvgGravity: 6.75, AvgRadius: 4880.25}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestSummariseEmptyDoesNotDivideByZero(t *testing.T) {
	if got := summarise(nil); got != (Summary{}) {
		t.Errorf("got %+v, want zero value", got)
	}
}

func TestClientSendsBearerKey(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"bodies":[{"bodyType":"Planet"}]}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, APIKey: "abc", HTTPClient: srv.Client(), Cache: cache.New[string, Summary](time.Minute)}
	s, err := c.Summary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer abc" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if s.Planets != 1 {
		t.Errorf("planets = %d", s.Planets)
	}
}

func TestSummaryCoalescesConcurrentMisses(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		time.Sleep(30 * time.Millisecond)
		_, _ = w.Write([]byte(`{"bodies":[]}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTPClient: srv.Client(), Cache: cache.New[string, Summary](time.Minute)}
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Summary(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Errorf("upstream called %d times, want 1", got)
	}
}
