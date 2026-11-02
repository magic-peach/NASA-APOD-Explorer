package config

import (
	"testing"
	"time"
)

func TestLoadFrom(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr bool
	}{
		{
			name: "defaults when nothing is set",
			env:  map[string]string{},
			want: Config{Port: "8080", NASAAPIKey: "DEMO_KEY", CacheTTL: 10 * time.Minute, DBPath: "app.db"},
		},
		{
			name: "env vars override defaults",
			env:  map[string]string{"PORT": "9000", "NASA_API_KEY": "k", "CACHE_TTL": "30s", "DB_PATH": "/data/x.db"},
			want: Config{Port: "9000", NASAAPIKey: "k", CacheTTL: 30 * time.Second, DBPath: "/data/x.db"},
		},
		{name: "unparseable TTL", env: map[string]string{"CACHE_TTL": "soon"}, wantErr: true},
		{name: "non-positive TTL", env: map[string]string{"CACHE_TTL": "0s"}, wantErr: true},
		{name: "bad rate limit", env: map[string]string{"RATE_LIMIT_RPS": "-1"}, wantErr: true},
		{name: "bad burst", env: map[string]string{"RATE_LIMIT_BURST": "many"}, wantErr: true},
		{name: "bad log format", env: map[string]string{"LOG_FORMAT": "xml"}, wantErr: true},
		{name: "short session secret", env: map[string]string{"SESSION_SECRET": "too-short"}, wantErr: true},
		{name: "bad cookie flag", env: map[string]string{"COOKIE_SECURE": "maybe"}, wantErr: true},
		{name: "bad demo flag", env: map[string]string{"DEMO_MODE": "sometimes"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LoadFrom(func(k string) string { return tt.env[k] })
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Compare only the fields this test is about.
			if got.Port != tt.want.Port || got.NASAAPIKey != tt.want.NASAAPIKey ||
				got.CacheTTL != tt.want.CacheTTL || got.DBPath != tt.want.DBPath {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSessionSecret(t *testing.T) {
	cfg, err := LoadFrom(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.SessionSecretGenerated || len(cfg.SessionSecret) != 32 {
		t.Errorf("missing SESSION_SECRET should generate a 32-byte secret, got %d bytes (generated=%v)",
			len(cfg.SessionSecret), cfg.SessionSecretGenerated)
	}
	if !cfg.CookieSecure {
		t.Error("cookies must be Secure by default")
	}

	given := "0123456789abcdef0123456789abcdef"
	cfg, err = LoadFrom(func(k string) string {
		if k == "SESSION_SECRET" {
			return given
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SessionSecretGenerated || string(cfg.SessionSecret) != given {
		t.Error("explicit SESSION_SECRET not used")
	}
}
