// Package apod is a small client for NASA's Astronomy Picture of the Day API
// (https://api.nasa.gov/planetary/apod).
package apod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// DefaultBaseURL is the production APOD endpoint.
const DefaultBaseURL = "https://api.nasa.gov/planetary/apod"

// Item is one APOD entry as returned by NASA.
type Item struct {
	Date        string `json:"date"`
	Title       string `json:"title"`
	Explanation string `json:"explanation"`
	URL         string `json:"url"`
	MediaType   string `json:"media_type"`
	HDURL       string `json:"hdurl,omitempty"`
}

// Query describes which APOD entries to fetch. Exactly one form is used,
// checked in this order: a date range, a single date, a random count, or
// (when everything is empty) today's entry.
type Query struct {
	Date      string // YYYY-MM-DD
	StartDate string // YYYY-MM-DD, used together with EndDate
	EndDate   string // YYYY-MM-DD
	Count     int    // number of random entries
}

// IsRange reports whether the query asks for a start/end date range.
func (q Query) IsRange() bool { return q.StartDate != "" && q.EndDate != "" }

// IsSingle reports whether NASA will answer this query with a single JSON
// object (one date, or today) rather than an array. It follows the same
// precedence as values(): range, then date, then count.
func (q Query) IsSingle() bool {
	switch {
	case q.IsRange():
		return false
	case q.Date != "":
		return true
	default:
		return q.Count == 0
	}
}

// values converts the query into NASA's URL parameters (without the key).
func (q Query) values() url.Values {
	v := url.Values{}
	switch {
	case q.IsRange():
		v.Set("start_date", q.StartDate)
		v.Set("end_date", q.EndDate)
	case q.Date != "":
		v.Set("date", q.Date)
	case q.Count > 0:
		v.Set("count", strconv.Itoa(q.Count))
	}
	return v
}

// Key returns a stable string that identifies the query. It is used as the
// cache key, and deliberately excludes the API key.
func (q Query) Key() string {
	return q.values().Encode() // Encode sorts keys, so equal queries give equal keys.
}

// HTTPClient fetches APOD data over HTTP. It is the concrete, production
// implementation; HTTP handlers depend on a small interface instead so
// tests can swap in a fake.
type HTTPClient struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// Fetch calls the APOD API and always returns a slice, normalising NASA's
// single-object and array response shapes into one type.
func (c *HTTPClient) Fetch(ctx context.Context, q Query) ([]Item, error) {
	params := q.values()
	params.Set("api_key", c.APIKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("apod: build request: %w", err)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		// Transport errors are *url.Error, whose message includes the full
		// request URL, and therefore the API key. Unwrap to the underlying
		// cause so the key can never end up in logs.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("apod: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // cap at 10 MiB
	if err != nil {
		return nil, fmt.Errorf("apod: read body: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("apod: upstream status %d", resp.StatusCode)
	}
	return decode(body)
}

// decode accepts either a JSON object or a JSON array of items.
func decode(body []byte) ([]Item, error) {
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") {
		var items []Item
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, fmt.Errorf("apod: decode array: %w", err)
		}
		return items, nil
	}
	var item Item
	if err := json.Unmarshal(body, &item); err != nil {
		return nil, fmt.Errorf("apod: decode object: %w", err)
	}
	return []Item{item}, nil
}
