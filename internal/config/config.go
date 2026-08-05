// Package config loads runtime settings from environment variables.
//
// Every setting has a sensible default so the app runs with zero
// configuration on a laptop, while production overrides values via env vars
// (12-factor style). Secrets are never hard-coded here: NASA_API_KEY falls
// back to NASA's public, heavily rate-limited DEMO_KEY.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds every runtime setting the server needs.
type Config struct {
	Port       string        // PORT: TCP port to listen on.
	NASAAPIKey string        // NASA_API_KEY: key for api.nasa.gov.
	CacheTTL   time.Duration // CACHE_TTL: how long upstream responses are cached, e.g. "10m".
	DBPath     string        // DB_PATH: path to the SQLite database file.

	// SolarSystemAPIKey is the free key for api.le-systeme-solaire.net
	// (SOLAR_SYSTEM_API_KEY). Optional: without it /api/space-facts
	// returns 502 and the UI shows a friendly "could not load" message.
	SolarSystemAPIKey string

	// AllowedOrigin is the one cross-origin site allowed to call the API
	// (APP_ORIGIN, e.g. "https://apod.example.com"). Empty means only
	// same-origin requests, which need no CORS headers at all.
	AllowedOrigin string

	// RateLimitRPS and RateLimitBurst configure the per-IP token bucket
	// for /api/ routes (RATE_LIMIT_RPS, RATE_LIMIT_BURST).
	RateLimitRPS   float64
	RateLimitBurst int

	// TrustedProxyHeader names a header set by a trusted reverse proxy that
	// carries the real client IP (TRUSTED_PROXY_HEADER, e.g. "Fly-Client-IP"
	// on Fly.io). Leave empty when not behind such a proxy: client-supplied
	// headers like X-Forwarded-For are trivially spoofed.
	TrustedProxyHeader string

	// LogFormat is "text" (human-friendly, default) or "json" (LOG_FORMAT).
	LogFormat string

	// PasswordSalt is the static salt used by the legacy SHA-256 password
	// hash (APP_SALT). It exists only to keep Phase 1 behaviour identical
	// and is removed when passwords move to bcrypt.
	PasswordSalt string

	// Optional LLM settings for the "explain this image" helpers. When
	// OpenAIAPIKey is empty the app uses built-in offline fallback text.
	OpenAIAPIKey string // OPENAI_API_KEY
	LLMModel     string // LLM_MODEL
	LLMBaseURL   string // LLM_BASE_URL
}

// Load reads the configuration from the process environment.
func Load() (Config, error) {
	return LoadFrom(os.Getenv)
}

// LoadFrom reads configuration using the supplied lookup function. Taking
// the lookup as a parameter (instead of calling os.Getenv directly) lets
// tests supply a fake environment without mutating global process state.
func LoadFrom(getenv func(string) string) (Config, error) {
	get := func(key, fallback string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return fallback
	}

	ttl, err := time.ParseDuration(get("CACHE_TTL", "10m"))
	if err != nil {
		return Config{}, fmt.Errorf("config: invalid CACHE_TTL: %w", err)
	}
	if ttl <= 0 {
		return Config{}, fmt.Errorf("config: CACHE_TTL must be positive, got %s", ttl)
	}

	rps, err := strconv.ParseFloat(get("RATE_LIMIT_RPS", "5"), 64)
	if err != nil || rps <= 0 {
		return Config{}, fmt.Errorf("config: RATE_LIMIT_RPS must be a positive number")
	}
	burst, err := strconv.Atoi(get("RATE_LIMIT_BURST", "20"))
	if err != nil || burst <= 0 {
		return Config{}, fmt.Errorf("config: RATE_LIMIT_BURST must be a positive integer")
	}
	logFormat := strings.ToLower(get("LOG_FORMAT", "text"))
	if logFormat != "text" && logFormat != "json" {
		return Config{}, fmt.Errorf("config: LOG_FORMAT must be text or json, got %q", logFormat)
	}

	return Config{
		Port:               get("PORT", "8080"),
		NASAAPIKey:         get("NASA_API_KEY", "DEMO_KEY"),
		CacheTTL:           ttl,
		DBPath:             get("DB_PATH", "app.db"),
		SolarSystemAPIKey:  getenv("SOLAR_SYSTEM_API_KEY"),
		AllowedOrigin:      strings.TrimRight(getenv("APP_ORIGIN"), "/"),
		RateLimitRPS:       rps,
		RateLimitBurst:     burst,
		TrustedProxyHeader: getenv("TRUSTED_PROXY_HEADER"),
		LogFormat:          logFormat,
		PasswordSalt:       get("APP_SALT", "nasa-apod-salt"),
		OpenAIAPIKey:       getenv("OPENAI_API_KEY"),
		LLMModel:           get("LLM_MODEL", "gpt-4o-mini"),
		LLMBaseURL:         get("LLM_BASE_URL", "https://api.openai.com/v1/chat/completions"),
	}, nil
}
