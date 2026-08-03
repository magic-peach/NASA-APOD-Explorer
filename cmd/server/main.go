// Command server runs the NASA APOD Explorer web app.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/magic-peach/NASA-APOD-Explorer/internal/apod"
	"github.com/magic-peach/NASA-APOD-Explorer/internal/cache"
	"github.com/magic-peach/NASA-APOD-Explorer/internal/config"
	"github.com/magic-peach/NASA-APOD-Explorer/internal/httpapi"
	"github.com/magic-peach/NASA-APOD-Explorer/internal/llm"
	"github.com/magic-peach/NASA-APOD-Explorer/internal/spacefacts"
	"github.com/magic-peach/NASA-APOD-Explorer/internal/store"
	"github.com/magic-peach/NASA-APOD-Explorer/web"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

// run wires dependencies and starts the server. Keeping this out of main
// means errors are returned (and tested) instead of calling os.Exit deep in
// the code.
func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx := context.Background()
	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	httpClient := &http.Client{Timeout: 15 * time.Second}

	apodClient := apod.NewCachedClient(
		&apod.HTTPClient{BaseURL: apod.DefaultBaseURL, APIKey: cfg.NASAAPIKey, HTTPClient: httpClient},
		cache.New[string, []apod.Item](cfg.CacheTTL),
	)
	facts := &spacefacts.Client{
		BaseURL:    spacefacts.DefaultBaseURL,
		APIKey:     cfg.SolarSystemAPIKey,
		HTTPClient: httpClient,
		Cache:      cache.New[string, spacefacts.Summary](cfg.CacheTTL),
	}
	assistant := &llm.Assistant{
		APIKey:     cfg.OpenAIAPIKey,
		Model:      cfg.LLMModel,
		BaseURL:    cfg.LLMBaseURL,
		HTTPClient: httpClient,
		Logger:     logger,
	}

	srv := httpapi.New(httpapi.Deps{
		APOD:         apodClient,
		Facts:        facts,
		Assistant:    assistant,
		Store:        db,
		Static:       web.Static(),
		PasswordSalt: cfg.PasswordSalt,
		Logger:       logger,
	})

	addr := ":" + cfg.Port
	logger.Info("NASA APOD Explorer running", "url", "http://localhost"+addr)
	return http.ListenAndServe(addr, srv.Handler())
}
