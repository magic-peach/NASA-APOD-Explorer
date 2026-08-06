package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/magic-peach/NASA-APOD-Explorer/internal/store"
)

// sessionStore maps opaque bearer tokens to user IDs, in memory.
// (Phase 4 replaces this with HTTP-only cookies backed by SQLite.)
type sessionStore struct {
	mu    sync.RWMutex
	items map[string]int64
}

func newSessionStore() *sessionStore {
	return &sessionStore{items: make(map[string]int64)}
}

func (s *sessionStore) create(userID int64) string {
	b := make([]byte, 24)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error on supported platforms
	token := hex.EncodeToString(b)
	s.mu.Lock()
	s.items[token] = userID
	s.mu.Unlock()
	return token
}

func (s *sessionStore) lookup(token string) (int64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.items[token]
	return id, ok
}

func (s *sessionStore) delete(token string) {
	s.mu.Lock()
	delete(s.items, token)
	s.mu.Unlock()
}

// hashPassword is the original salted SHA-256 scheme, kept unchanged for
// now. (Phase 4 replaces it with bcrypt.)
func (s *Server) hashPassword(password string) string {
	sum := sha256.Sum256([]byte(s.PasswordSalt + ":" + password))
	return hex.EncodeToString(sum[:])
}

func bearerToken(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// currentUser returns the logged-in user's ID, if any.
func (s *Server) currentUser(r *http.Request) (int64, bool) {
	return s.sessions.lookup(bearerToken(r))
}

// authedHandler is a handler that receives the authenticated user's ID.
type authedHandler func(w http.ResponseWriter, r *http.Request, userID int64)

// requireAuth rejects requests without a valid session and passes the user
// ID to next otherwise. Passing it as an argument (rather than through the
// request context) makes the dependency explicit and type-safe.
func (s *Server) requireAuth(next authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := s.currentUser(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, codeUnauthorized, "please log in first")
			return
		}
		next(w, r, userID)
	}
}

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &in); err != nil || in.Username == "" || in.Email == "" || len(in.Password) < 6 {
		writeError(w, badRequest(codeBadRequest, "username, email and a password of at least 6 characters are required"))
		return
	}
	_, err := s.Store.CreateUser(r.Context(), in.Username, in.Email, s.hashPassword(in.Password))
	if errors.Is(err, store.ErrConflict) {
		writeErr(w, http.StatusConflict, codeConflict, "that username or email is already registered")
		return
	}
	if err != nil {
		s.internalError(w, r, "signup", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, badRequest(codeBadRequest, "request body must be valid JSON"))
		return
	}
	u, err := s.Store.UserByLogin(r.Context(), in.Username)
	if err != nil || u.PasswordHash != s.hashPassword(in.Password) {
		// Same message for "no such user" and "wrong password" so the
		// response doesn't reveal which usernames exist.
		writeErr(w, http.StatusUnauthorized, codeUnauthorized, "invalid username or password")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": s.sessions.create(u.ID)})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.sessions.delete(bearerToken(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
