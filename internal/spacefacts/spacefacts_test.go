package spacefacts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
