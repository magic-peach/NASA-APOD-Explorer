// Package spacefacts computes a few headline numbers about the solar system
// from the public Solar System OpenData API.
package spacefacts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/magic-peach/NASA-APOD-Explorer/internal/cache"
)

// DefaultBaseURL is the production endpoint listing every known body.
const DefaultBaseURL = "https://api.le-systeme-solaire.net/rest/bodies/"

// Summary is the aggregate returned to the browser.
type Summary struct {
	Planets    int     `json:"planets"`
	Moons      int     `json:"moons"`
	AvgGravity float64 `json:"avg_gravity"` // m/s²
	AvgRadius  float64 `json:"avg_radius"`  // km
}

// Client fetches and summarises solar-system data, caching the result.
type Client struct {
	BaseURL    string
	APIKey     string // sent as "Authorization: Bearer <key>"; the API rejects requests without one
	HTTPClient *http.Client
	Cache      *cache.Cache[string, Summary]
}

// body is the subset of fields we read from each upstream record.
type body struct {
	BodyType   string            `json:"bodyType"`
	Moons      []json.RawMessage `json:"moons"`
	Gravity    *float64          `json:"gravity"`
	MeanRadius *float64          `json:"meanRadius"`
}

// Summary returns the cached summary or fetches and computes a fresh one.
func (c *Client) Summary(ctx context.Context) (Summary, error) {
	const key = "summary"
	if s, ok := c.Cache.Get(key); ok {
		return s, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL, nil)
	if err != nil {
		return Summary{}, fmt.Errorf("spacefacts: build request: %w", err)
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return Summary{}, fmt.Errorf("spacefacts: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return Summary{}, fmt.Errorf("spacefacts: upstream status %d", resp.StatusCode)
	}

	var payload struct {
		Bodies []body `json:"bodies"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 20<<20)).Decode(&payload); err != nil {
		return Summary{}, fmt.Errorf("spacefacts: decode: %w", err)
	}

	s := summarise(payload.Bodies)
	c.Cache.Set(key, s)
	return s, nil
}

// summarise is a pure function so it can be unit tested without HTTP.
func summarise(bodies []body) Summary {
	var s Summary
	var gravitySum, radiusSum float64
	var gravityN, radiusN int
	for _, b := range bodies {
		if strings.EqualFold(b.BodyType, "Planet") {
			s.Planets++
		}
		s.Moons += len(b.Moons)
		if b.Gravity != nil {
			gravitySum += *b.Gravity
			gravityN++
		}
		if b.MeanRadius != nil {
			radiusSum += *b.MeanRadius
			radiusN++
		}
	}
	s.AvgGravity = round2(safeDivide(gravitySum, float64(gravityN)))
	s.AvgRadius = round2(safeDivide(radiusSum, float64(radiusN)))
	return s
}

func safeDivide(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
