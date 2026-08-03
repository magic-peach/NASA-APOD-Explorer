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
