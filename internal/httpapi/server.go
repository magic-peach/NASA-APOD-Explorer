// Package httpapi exposes the JSON API and serves the embedded frontend.
package httpapi

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"

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

// Deps are the collaborators a Server needs.
type Deps struct {
	APOD         APODClient
	Facts        FactsClient
	Assistant    Assistant
	Store        *store.Store
	Static       fs.FS // frontend files, usually the embedded web assets
	PasswordSalt string
	Logger       *slog.Logger
}

// Server holds handler dependencies.
type Server struct {
	Deps
	sessions *sessionStore
}

// New builds a Server from its dependencies.
func New(d Deps) *Server {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Server{Deps: d, sessions: newSessionStore()}
}

// Handler returns the fully wired HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Go 1.22+ patterns can include the method, so the mux itself answers
	// 405 Method Not Allowed for anything not listed.
	mux.HandleFunc("POST /api/signup", s.handleSignup)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)

	mux.HandleFunc("GET /api/apod", s.handleAPOD)
	mux.HandleFunc("GET /api/space-facts", s.handleSpaceFacts)
	mux.HandleFunc("POST /api/llm", s.handleLLM)

	mux.HandleFunc("GET /api/favorites", s.requireAuth(s.handleListFavorites))
	mux.HandleFunc("POST /api/favorites", s.requireAuth(s.handleAddFavorite))
	mux.HandleFunc("GET /api/history", s.requireAuth(s.handleHistory))
	mux.HandleFunc("GET /api/comments", s.handleListComments)
	mux.HandleFunc("POST /api/comments", s.requireAuth(s.handleAddComment))
	mux.HandleFunc("GET /api/ratings", s.handleAverageRating)
	mux.HandleFunc("POST /api/ratings", s.requireAuth(s.handleSetRating))

	mux.Handle("GET /", http.FileServerFS(s.Static))

	return withCORS(mux)
}

// writeJSON encodes v as the JSON response body with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // the client may have gone away; nothing useful to do
}

// decodeJSON reads a JSON request body into dst, rejecting bodies over 1 MiB.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	return json.NewDecoder(r.Body).Decode(dst)
}

// withCORS adds permissive CORS headers. (Phase 3 restricts this to the
// app's own origin.)
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
