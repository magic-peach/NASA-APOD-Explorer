// Package httpapi exposes the JSON API and serves the embedded frontend.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/magic-peach/NASA-APOD-Explorer/internal/apod"
	"github.com/magic-peach/NASA-APOD-Explorer/internal/llm"
	"github.com/magic-peach/NASA-APOD-Explorer/internal/spacefacts"
	"github.com/magic-peach/NASA-APOD-Explorer/internal/store"
)

// The interfaces below are defined here, by the consumer, rather than in the
// packages that implement them. That is idiomatic Go ("accept interfaces,
// return structs"): handlers declare exactly the behaviour they need, and
// tests can satisfy it with a tiny fake instead of hitting real APIs.

// APODClient fetches Astronomy Picture of the Day entries.
type APODClient interface {
	Fetch(ctx context.Context, q apod.Query) ([]apod.Item, error)
}

// FactsClient returns aggregate solar-system facts.
type FactsClient interface {
	Summary(ctx context.Context) (spacefacts.Summary, error)
}

// Assistant answers "explain this image" helper requests.
type Assistant interface {
	Run(ctx context.Context, req llm.Request) llm.Response
}

// upstreamTimeout bounds how long one API request waits on NASA or another
// upstream service before giving up with 504. It stays below the server's
// WriteTimeout (30s) so the client always gets a proper JSON error.
const upstreamTimeout = 20 * time.Second

// Deps are the collaborators and settings a Server needs.
type Deps struct {
	APOD      APODClient
	Facts     FactsClient
	Assistant Assistant
	Store     *store.Store
	Static    fs.FS // frontend files, usually the embedded web assets
	Logger    *slog.Logger

	SessionSecret      []byte        // HMAC key for stored session tokens
	SessionTTL         time.Duration // login lifetime (default 30 days)
	CookieSecure       bool          // set the Secure flag on the session cookie
	BcryptCost         int           // bcrypt work factor (default bcrypt.DefaultCost)
	LegacyPasswordSalt string        // verifies pre-bcrypt password hashes

	AllowedOrigin      string  // extra origin allowed by CORS; "" = same-origin only
	RateLimitRPS       float64 // per-IP requests per second on /api/ (default 5)
	RateLimitBurst     int     // per-IP burst allowance (default 20)
	TrustedProxyHeader string  // header carrying the real client IP behind a proxy

	// Now returns the current time; tests override it to pin "today".
	Now func() time.Time
}

// Server holds handler dependencies.
type Server struct {
	Deps
	limiter *ipRateLimiter

	dummyOnce sync.Once // lazily computes dummy (see dummyHash)
	dummy     []byte
}

// New builds a Server from its dependencies, filling in defaults.
func New(d Deps) *Server {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.RateLimitRPS <= 0 {
		d.RateLimitRPS = 5
	}
	if d.RateLimitBurst <= 0 {
		d.RateLimitBurst = 20
	}
	if d.SessionTTL <= 0 {
		d.SessionTTL = 30 * 24 * time.Hour
	}
	if d.BcryptCost == 0 {
		d.BcryptCost = bcrypt.DefaultCost
	}
	return &Server{
		Deps:    d,
		limiter: newIPRateLimiter(d.RateLimitRPS, d.RateLimitBurst),
	}
}

// RunBackground runs the server's maintenance loops (pruning idle rate
// limiters and expired sessions) until ctx is cancelled. It blocks until
// both loops have exited; call it in a goroutine.
func (s *Server) RunBackground(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.limiter.runCleanup(ctx) }()
	go func() { defer wg.Done(); s.purgeExpiredSessions(ctx) }()
	wg.Wait()
}

// Handler returns the fully wired HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Probes: liveness says "the process is up"; readiness says "and it can
	// serve traffic", which for us means the database answers.
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)

	// Go 1.22+ patterns can include the method, so the mux itself answers
	// 405 Method Not Allowed for anything not listed.
	mux.HandleFunc("POST /api/auth/signup", s.handleSignup)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/auth/me", s.requireAuth(s.handleMe))

	mux.HandleFunc("GET /api/apod", s.handleAPOD)
	mux.HandleFunc("GET /api/apod/on-this-day", s.handleOnThisDay)
	mux.HandleFunc("GET /api/space-facts", s.handleSpaceFacts)
	mux.HandleFunc("POST /api/llm", s.handleLLM)

	mux.HandleFunc("GET /api/favorites", s.requireAuth(s.handleListFavorites))
	mux.HandleFunc("POST /api/favorites", s.requireAuth(s.handleAddFavorite))
	mux.HandleFunc("DELETE /api/favorites/{date}", s.requireAuth(s.handleDeleteFavorite))
	mux.HandleFunc("GET /api/history", s.requireAuth(s.handleHistory))
	mux.HandleFunc("POST /api/history", s.requireAuth(s.handleRecordView))
	mux.HandleFunc("GET /api/comments", s.handleListComments)
	mux.HandleFunc("POST /api/comments", s.requireAuth(s.handleAddComment))
	mux.HandleFunc("GET /api/ratings", s.handleAverageRating)
	mux.HandleFunc("POST /api/ratings", s.requireAuth(s.handleSetRating))

	// Unknown /api/ paths get a JSON 404 instead of falling through to the
	// static file server's HTML-ish "404 page not found".
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeErr(w, http.StatusNotFound, codeNotFound, "no such API endpoint")
	})
	// Registered without a method: Go's router rejects "GET /" next to
	// "/api/" as ambiguous (neither pattern is strictly more specific).
	mux.Handle("/", http.FileServerFS(s.Static))

	// Outermost first: every request gets an ID, then is logged; panics
	// are recovered inside logging so the 500 shows up in the log line.
	return chain(mux,
		withRequestID,
		withLogging(s.Logger),
		withRecovery(s.Logger),
		withCORS(s.AllowedOrigin),
		withOriginCheck(s.AllowedOrigin),
		withRateLimit(s.limiter, clientIP(s.TrustedProxyHeader)),
	)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.Store.Ping(ctx); err != nil {
		s.log(r).Warn("readiness check failed", "err", err)
		writeErr(w, http.StatusServiceUnavailable, "not_ready", "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// log returns the server logger annotated with the request's ID, so every
// line a handler writes can be tied back to the access-log line.
func (s *Server) log(r *http.Request) *slog.Logger {
	return s.Logger.With("request_id", requestID(r.Context()))
}

// writeJSON encodes v as the JSON response body with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // the client may have gone away; nothing useful to do
}

// errNotJSON is returned by decodeJSON for a non-JSON Content-Type.
var errNotJSON = errors.New("content type must be application/json")

// decodeJSON reads a JSON request body into dst. It requires a JSON
// Content-Type (an HTML form on another site cannot send one without a CORS
// preflight, which blunts CSRF), caps the body at 1 MiB, and rejects
// unknown fields so typos fail loudly instead of being silently ignored.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		return errNotJSON
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
