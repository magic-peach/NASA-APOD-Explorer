package apod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ScienceBaseURL is the APOD feed on science.nasa.gov, where APOD has lived
// since it moved off apod.nasa.gov. Unlike api.nasa.gov it returns the real
// picture for every date, and it needs no API key.
const ScienceBaseURL = "https://science.nasa.gov/wp-json/wp/v2/apod-basic"

// firstAPOD is the earliest date in the APOD archive.
var firstAPOD = time.Date(1995, time.June, 16, 0, 0, 0, 0, time.UTC)

// ErrNoEntry means the archive has no entry for a date. The early archive
// has gaps (for example 1995-06-17 to 1995-06-19).
var ErrNoEntry = errors.New("apod: no entry for that date")

// maxParallel caps how many dates a ScienceClient fetches at once for a
// range or a random pick, to stay polite to science.nasa.gov.
const maxParallel = 6

// ScienceClient fetches APOD entries from the science.nasa.gov feed. The
// feed answers one date at a time (/YYMMDD) or lists the newest entries, so
// ranges and random picks are built from parallel single-date requests.
type ScienceClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// scienceEntry is the subset of the feed's JSON that we use. In the feed,
// "url" is the article page; the picture itself is "hdurl".
type scienceEntry struct {
	Date        string `json:"date"`
	Title       string `json:"title"`
	MediaType   string `json:"media_type"`
	Explanation string `json:"explanation"`
	Copyright   string `json:"copyright"`
	HDURL       string `json:"hdurl"`
	BasicHTML   string `json:"basic_html"`
}

// Fetch implements Fetcher.
func (c *ScienceClient) Fetch(ctx context.Context, q Query) ([]Item, error) {
	switch {
	case q.IsRange():
		return c.fetchRange(ctx, q.StartDate, q.EndDate)
	case q.Date != "":
		it, err := c.fetchDate(ctx, q.Date)
		if err != nil {
			return nil, err
		}
		return []Item{it}, nil
	case q.Count > 0:
		return c.fetchRandom(ctx, q.Count)
	default:
		return c.fetchLatest(ctx)
	}
}

func (c *ScienceClient) fetchLatest(ctx context.Context) ([]Item, error) {
	var entries []scienceEntry
	if err := c.get(ctx, c.BaseURL+"?per_page=1", &entries); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, errors.New("apod: science feed returned no entries")
	}
	it, err := entries[0].item()
	if err != nil {
		return nil, err
	}
	return []Item{it}, nil
}

func (c *ScienceClient) fetchDate(ctx context.Context, date string) (Item, error) {
	d, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return Item{}, fmt.Errorf("apod: bad date %q", date)
	}
	var e scienceEntry
	if err := c.get(ctx, c.BaseURL+"/"+d.Format("060102"), &e); err != nil {
		return Item{}, err
	}
	return e.item()
}

// fetchRange returns the entries from start to end inclusive, oldest first
// (the same order as api.nasa.gov). Dates with no entry are skipped.
func (c *ScienceClient) fetchRange(ctx context.Context, start, end string) ([]Item, error) {
	s, err1 := time.Parse(time.DateOnly, start)
	e, err2 := time.Parse(time.DateOnly, end)
	if err1 != nil || err2 != nil || e.Before(s) {
		return nil, fmt.Errorf("apod: bad range %q to %q", start, end)
	}
	var dates []string
	for d := s; !d.After(e); d = d.AddDate(0, 0, 1) {
		dates = append(dates, d.Format(time.DateOnly))
	}
	items, err := c.fetchMany(ctx, dates)
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Date < items[j].Date })
	return items, nil
}

// fetchRandom returns up to n entries from random archive dates. A few
// extra dates are tried so the odd gap in the archive doesn't leave the
// answer short.
func (c *ScienceClient) fetchRandom(ctx context.Context, n int) ([]Item, error) {
	days := int(time.Now().UTC().Sub(firstAPOD).Hours() / 24)
	seen := map[string]bool{}
	var dates []string
	for len(dates) < n+3 {
		d := firstAPOD.AddDate(0, 0, rand.IntN(days)).Format(time.DateOnly) //nolint:gosec // picking a random picture, not a secret
		if !seen[d] {
			seen[d] = true
			dates = append(dates, d)
		}
	}
	items, err := c.fetchMany(ctx, dates)
	if err != nil {
		return nil, err
	}
	if len(items) > n {
		items = items[:n]
	}
	return items, nil
}

// fetchMany fetches dates in parallel, skipping dates with no entry. It
// fails only if no date could be fetched at all.
func (c *ScienceClient) fetchMany(ctx context.Context, dates []string) ([]Item, error) {
	type result struct {
		item Item
		err  error
	}
	results := make([]result, len(dates))
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i, d := range dates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			it, err := c.fetchDate(ctx, d)
			results[i] = result{it, err}
		}()
	}
	wg.Wait()

	var items []Item
	var firstErr error
	for _, r := range results {
		switch {
		case r.err == nil:
			items = append(items, r.item)
		case errors.Is(r.err, ErrNoEntry):
		case firstErr == nil:
			firstErr = r.err
		}
	}
	if len(items) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return items, nil
}

// get fetches url and decodes its JSON body into v.
func (c *ScienceClient) get(ctx context.Context, rawURL string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("apod: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("apod: science feed request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("apod: read body: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrNoEntry
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("apod: science feed status %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("apod: decode science feed: %w", err)
	}
	return nil
}

var (
	tagRe        = regexp.MustCompile(`<[^>]*>`)
	spaceRe      = regexp.MustCompile(`\s+`)
	iframeSrcRe  = regexp.MustCompile(`(?i)<iframe[^>]*\ssrc="([^"]+)"`)
	sourceSrcRe  = regexp.MustCompile(`(?i)<source[^>]*\ssrc="([^"]+)"`)
	ogVideoRe    = regexp.MustCompile(`(?i)<meta property="og:video" content="([^"]+)"`)
	tomorrowRe   = regexp.MustCompile(`(?i)\s*Tomorrow's picture:.*$`)
	explPrefixRe = regexp.MustCompile(`(?i)^\s*Explanation:\s*`)
)

// plainText turns a fragment of the feed's HTML into plain text.
func plainText(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

// item converts a feed entry into an Item shaped like api.nasa.gov's.
func (e scienceEntry) item() (Item, error) {
	if e.Date == "" {
		return Item{}, errors.New("apod: science feed entry has no date")
	}
	expl := plainText(e.Explanation)
	expl = explPrefixRe.ReplaceAllString(expl, "")
	expl = tomorrowRe.ReplaceAllString(expl, "")

	it := Item{
		Date:        e.Date,
		Title:       plainText(e.Title),
		Explanation: expl,
		MediaType:   e.MediaType,
		Copyright:   plainText(e.Copyright),
	}
	if it.MediaType == "video" {
		it.URL = videoURL(e.BasicHTML)
		it.ThumbnailURL = e.HDURL // for videos the feed's hdurl is the poster
		if it.URL == "" {
			return Item{}, fmt.Errorf("apod: no video found for %s", e.Date)
		}
		return it, nil
	}
	if e.HDURL == "" {
		return Item{}, fmt.Errorf("apod: no image found for %s", e.Date)
	}
	it.MediaType = "image"
	it.URL = e.HDURL
	it.HDURL = e.HDURL
	return it, nil
}

// videoURL finds the video in an entry's page: an embedded player, a video
// file, or failing that the page's og:video tag.
func videoURL(page string) string {
	for _, re := range []*regexp.Regexp{iframeSrcRe, sourceSrcRe, ogVideoRe} {
		if m := re.FindStringSubmatch(page); m != nil {
			u := html.UnescapeString(m[1])
			if strings.HasPrefix(u, "//") {
				u = "https:" + u
			}
			return u
		}
	}
	return ""
}

// Fallback tries Primary and, if that fails, Secondary.
type Fallback struct {
	Primary, Secondary Fetcher
}

// Fetch implements Fetcher.
func (f *Fallback) Fetch(ctx context.Context, q Query) ([]Item, error) {
	items, err := f.Primary.Fetch(ctx, q)
	if err == nil && len(items) > 0 {
		return items, nil
	}
	items2, err2 := f.Secondary.Fetch(ctx, q)
	if err2 != nil {
		if err == nil {
			err = errors.New("empty response")
		}
		return nil, fmt.Errorf("%w; fallback: %w", err, err2)
	}
	return items2, nil
}
