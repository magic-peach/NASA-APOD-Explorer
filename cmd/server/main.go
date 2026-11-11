// Command server runs the NASA APOD Explorer web app.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
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

// shutdownTimeout is how long in-flight requests get to finish after a
// stop signal before the server closes their connections anyway.
const shutdownTimeout = 15 * time.Second

func main() {
	// "server healthcheck" is used by the Docker HEALTHCHECK: the runtime
	// image is distroless (no shell, no curl), so the binary checks itself.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		port := os.Getenv("PORT")
		if port == "" {
			port = "8080"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := healthcheck(ctx, "http://127.0.0.1:"+port+"/healthz")
		cancel() // called explicitly: os.Exit below would skip a deferred call
		if err != nil {
			fmt.Fprintln(os.Stderr, "unhealthy:", err)
			os.Exit(1)
		}
		return
	}

	// ctx is cancelled on Ctrl+C (SIGINT) or SIGTERM, which is what Docker,
	// Kubernetes and Fly.io send when stopping a container.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "server error:", err)
		os.Exit(1)
	}
}

// run wires dependencies and serves until ctx is cancelled. Keeping this out
// of main means errors are returned instead of calling os.Exit deep inside.
func run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := newLogger(cfg.LogFormat)
	slog.SetDefault(logger)
	// Vercel sets VERCEL=1. There, instances come and go and several run at
	// once, so two laptop-friendly fallbacks would fail silently: a random
	// session secret per instance (logins rejected by the next instance)
	// and a SQLite file on the instance's temporary disk (data lost at
	// random). Refuse to start instead.
	if os.Getenv("VERCEL") == "1" {
		if cfg.SessionSecretGenerated {
			return errors.New("SESSION_SECRET must be set on Vercel")
		}
		if !store.IsRemote(cfg.DBPath) {
			return errors.New("DB_PATH must be a libsql:// URL on Vercel")
		}
	}
	if cfg.SessionSecretGenerated {
		logger.Warn("SESSION_SECRET not set; using a random one, so restarts will log everyone out")
	}

	db, err := store.Open(ctx, cfg.DBPath, cfg.DBAuthToken)
	if err != nil {
		return err
	}
	defer db.Close() // runs last, after the server and background work have stopped

	// Backstop timeout; each request also sets its own context deadline.
	httpClient := &http.Client{Timeout: 30 * time.Second}

	apodCache := cache.New[string, []apod.Item](cfg.CacheTTL)
	factsCache := cache.New[string, spacefacts.Summary](cfg.CacheTTL)

	// Either the real NASA client or the built-in samples; both satisfy the
	// same interface, so nothing downstream needs to know which.
	var upstream apod.Fetcher = &apod.HTTPClient{BaseURL: apod.DefaultBaseURL, APIKey: cfg.NASAAPIKey, HTTPClient: httpClient}
	if cfg.DemoMode {
		upstream = &apod.DemoClient{}
		logger.Info("DEMO_MODE on: serving built-in sample images instead of calling NASA")
	}

	srv := httpapi.New(httpapi.Deps{
		APOD: apod.NewCachedClient(upstream, apodCache),
		Facts: &spacefacts.Client{
			BaseURL:    spacefacts.DefaultBaseURL,
			APIKey:     cfg.SolarSystemAPIKey,
			HTTPClient: httpClient,
			Cache:      factsCache,
		},
		Assistant: &llm.Assistant{
			APIKey:     cfg.OpenAIAPIKey,
			Model:      cfg.LLMModel,
			BaseURL:    cfg.LLMBaseURL,
			HTTPClient: httpClient,
			Logger:     logger,
		},
		Store:              db,
		Static:             web.Static(),
		SessionSecret:      cfg.SessionSecret,
		SessionTTL:         cfg.SessionTTL,
		CookieSecure:       cfg.CookieSecure,
		LegacyPasswordSalt: cfg.LegacyPasswordSalt,
		Logger:             logger,
		AllowedOrigin:      cfg.AllowedOrigin,
		RateLimitRPS:       cfg.RateLimitRPS,
		RateLimitBurst:     cfg.RateLimitBurst,
		TrustedProxyHeader: cfg.TrustedProxyHeader,
	})

	// Background goroutines all stop when ctx is cancelled. The WaitGroup
	// lets us wait for them to actually exit before closing the database.
	var wg sync.WaitGroup
	background := []func(context.Context){
		func(ctx context.Context) { apodCache.RunJanitor(ctx, time.Minute) },
		func(ctx context.Context) { factsCache.RunJanitor(ctx, time.Minute) },
		srv.RunBackground,
	}
	for _, task := range background {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task(ctx)
		}()
	}
	defer wg.Wait()

	httpServer := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: srv.Handler(),

		// Without explicit timeouts net/http waits forever, so a client
		// that opens connections and sends bytes very slowly can exhaust
		// the server ("slowloris").
		ReadHeaderTimeout: 5 * time.Second,   // time to send request headers
		ReadTimeout:       15 * time.Second,  // time to send the whole request
		WriteTimeout:      30 * time.Second,  // time to produce the response (covers upstream calls)
		IdleTimeout:       120 * time.Second, // how long a keep-alive connection may sit idle

		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", httpServer.Addr)
	if err != nil {
		return err
	}
	logger.Info("NASA APOD Explorer running", "url", "http://localhost:"+cfg.Port)
	return serve(ctx, httpServer, ln, logger)
}

// serve runs httpServer on ln until ctx is cancelled, then shuts down
// gracefully: it stops accepting new connections, waits up to
// shutdownTimeout for in-flight requests to finish, and only then returns.
// It takes a listener (rather than calling ListenAndServe) so tests can
// use a random port.
func serve(ctx context.Context, httpServer *http.Server, ln net.Listener, logger *slog.Logger) error {
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(ln) }()

	select {
	case err := <-serveErr:
		// The server stopped on its own, which only happens on error.
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down, waiting for in-flight requests", "timeout", shutdownTimeout.String())
	// ctx is already cancelled, so shutdown needs a fresh context for its
	// own deadline.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	// Serve returns http.ErrServerClosed after Shutdown; that's success.
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	logger.Info("server stopped cleanly")
	return nil
}

// healthcheck GETs url and fails unless it answers 200 before ctx ends.
func healthcheck(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func newLogger(format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}
