package apod

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeFeed serves a tiny science.nasa.gov feed: an image on 2026-11-12, a
// YouTube video on 2026-11-13, an mp4 on 2026-11-14, and 404 elsewhere.
func fakeFeed(t *testing.T) *httptest.Server {
	t.Helper()
	image := `{"date":"2026-11-12","title":"Moon, Venus &amp; Spica","media_type":"image",
		"explanation":"<strong>Explanation:</strong> <a href=\"x\">Not all</a> who wander.<br><br><strong>Tomorrow's picture:</strong> what's next?",
		"copyright":"<a href=\"y\">Włodzimierz Bubak</a>",
		"url":"https://science.nasa.gov/image-article/x/","hdurl":"https://assets.example/conjunction.jpg"}`
	youtube := `{"date":"2026-11-13","title":"Clip","media_type":"video","explanation":"E","copyright":"",
		"hdurl":"https://assets.example/poster.jpg",
		"basic_html":"<center><iframe width=\"960\" src=\"//www.youtube.com/embed/6_cH5-daLjg?rel=0\"></iframe></center>"}`
	mp4 := `{"date":"2026-11-14","title":"File","media_type":"video","explanation":"E","copyright":"",
		"hdurl":"https://assets.example/poster2.jpg",
		"basic_html":"<video controls><source src=\"https://assets.example/clip.mp4\" type=\"video/mp4\"></video>"}`
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/" && r.URL.Query().Get("per_page") == "1":
			_, _ = w.Write([]byte("[" + mp4 + "]"))
		case r.URL.Path == "/261112":
			_, _ = w.Write([]byte(image))
		case r.URL.Path == "/261113":
			_, _ = w.Write([]byte(youtube))
		case r.URL.Path == "/261114":
			_, _ = w.Write([]byte(mp4))
		case r.URL.Path == "/261115":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"not_found"}`))
		}
	}))
}

func TestScienceClientImage(t *testing.T) {
	srv := fakeFeed(t)
	defer srv.Close()
	c := &ScienceClient{BaseURL: srv.URL, HTTPClient: srv.Client()}

	items, err := c.Fetch(context.Background(), Query{Date: "2026-11-12"})
	if err != nil {
		t.Fatal(err)
	}
	it := items[0]
	if it.Title != "Moon, Venus & Spica" {
		t.Errorf("Title = %q", it.Title)
	}
	if it.Explanation != "Not all who wander." {
		t.Errorf("Explanation = %q", it.Explanation)
	}
	if it.Copyright != "Włodzimierz Bubak" {
		t.Errorf("Copyright = %q", it.Copyright)
	}
	if it.MediaType != "image" || it.URL != "https://assets.example/conjunction.jpg" || it.HDURL != it.URL {
		t.Errorf("image fields = %+v", it)
	}
	if IsPlaceholder(it) {
		t.Error("real entry flagged as placeholder")
	}
}

func TestScienceClientVideos(t *testing.T) {
	srv := fakeFeed(t)
	defer srv.Close()
	c := &ScienceClient{BaseURL: srv.URL, HTTPClient: srv.Client()}

	items, err := c.Fetch(context.Background(), Query{Date: "2026-11-13"})
	if err != nil {
		t.Fatal(err)
	}
	if got := items[0]; got.URL != "https://www.youtube.com/embed/6_cH5-daLjg?rel=0" || got.ThumbnailURL != "https://assets.example/poster.jpg" {
		t.Errorf("youtube entry = %+v", got)
	}

	items, err = c.Fetch(context.Background(), Query{}) // latest
	if err != nil {
		t.Fatal(err)
	}
	if got := items[0]; got.Date != "2026-11-14" || got.URL != "https://assets.example/clip.mp4" {
		t.Errorf("latest entry = %+v", got)
	}
}

func TestScienceClientRangeSkipsGaps(t *testing.T) {
	srv := fakeFeed(t)
	defer srv.Close()
	c := &ScienceClient{BaseURL: srv.URL, HTTPClient: srv.Client()}

	items, err := c.Fetch(context.Background(), Query{StartDate: "2026-11-10", EndDate: "2026-11-14"})
	if err != nil {
		t.Fatal(err)
	}
	var dates []string
	for _, it := range items {
		dates = append(dates, it.Date)
	}
	if got := strings.Join(dates, ","); got != "2026-11-12,2026-11-13,2026-11-14" {
		t.Errorf("dates = %s", got)
	}
}

func TestScienceClientErrors(t *testing.T) {
	srv := fakeFeed(t)
	defer srv.Close()
	c := &ScienceClient{BaseURL: srv.URL, HTTPClient: srv.Client()}

	if _, err := c.Fetch(context.Background(), Query{Date: "2026-11-01"}); !errors.Is(err, ErrNoEntry) {
		t.Errorf("missing date: err = %v, want ErrNoEntry", err)
	}
	if _, err := c.Fetch(context.Background(), Query{Date: "2026-11-15"}); err == nil || errors.Is(err, ErrNoEntry) {
		t.Errorf("server error: err = %v", err)
	}
	if _, err := c.Fetch(context.Background(), Query{StartDate: "2026-11-15", EndDate: "2026-11-16"}); err == nil {
		t.Error("range where every date fails should be an error")
	}
}

func TestScienceClientRandom(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		date := "20" + r.URL.Path[1:3] + "-" + r.URL.Path[3:5] + "-" + r.URL.Path[5:7]
		_, _ = w.Write([]byte(`{"date":"` + date + `","title":"T","media_type":"image","hdurl":"https://a/x.jpg"}`))
	}))
	defer srv.Close()
	c := &ScienceClient{BaseURL: srv.URL, HTTPClient: srv.Client()}

	items, err := c.Fetch(context.Background(), Query{Count: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Errorf("got %d items, want 4", len(items))
	}
}

type stubFetcher struct {
	items []Item
	err   error
	calls int
}

func (s *stubFetcher) Fetch(context.Context, Query) ([]Item, error) {
	s.calls++
	return s.items, s.err
}

func TestFallback(t *testing.T) {
	good := &stubFetcher{items: []Item{{Date: "2026-11-12"}}}
	bad := &stubFetcher{err: errors.New("down")}

	f := &Fallback{Primary: good, Secondary: bad}
	if _, err := f.Fetch(context.Background(), Query{}); err != nil || bad.calls != 0 {
		t.Errorf("primary ok: err=%v secondary calls=%d", err, bad.calls)
	}

	backup := &stubFetcher{items: []Item{{Date: "backup"}}}
	f = &Fallback{Primary: bad, Secondary: backup}
	items, err := f.Fetch(context.Background(), Query{})
	if err != nil || items[0].Date != "backup" {
		t.Errorf("fallback: items=%v err=%v", items, err)
	}

	f = &Fallback{Primary: bad, Secondary: &stubFetcher{err: errors.New("also down")}}
	if _, err := f.Fetch(context.Background(), Query{}); err == nil || !strings.Contains(err.Error(), "also down") {
		t.Errorf("both down: err = %v", err)
	}
}
