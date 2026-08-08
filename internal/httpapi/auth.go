package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/magic-peach/NASA-APOD-Explorer/internal/store"
)

// sessionCookieName is the cookie holding the opaque session token.
const sessionCookieName = "apod_session"

// Password rules. bcrypt only looks at the first 72 bytes of its input, so
// longer passwords are rejected rather than silently truncated.
const (
	minPasswordLen = 8
	maxPasswordLen = 72
)

var validUsername = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,32}$`)

// ---- passwords -----------------------------------------------------------

// hashPassword hashes a new password with bcrypt. bcrypt is deliberately
// slow (tunable via the cost) and salts each hash randomly, which makes
// brute-forcing a stolen database far more expensive than a fast hash like
// SHA-256.
func (s *Server) hashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), s.BcryptCost)
	return string(h), err
}

// checkPassword reports whether password matches the stored hash, and
// whether the stored hash uses the legacy scheme and should be upgraded.
func (s *Server) checkPassword(stored, password string) (ok, legacy bool) {
	if strings.HasPrefix(stored, "$2") { // bcrypt hashes start with $2a$, $2b$ or $2y$
		return bcrypt.CompareHashAndPassword([]byte(stored), []byte(password)) == nil, false
	}
	// Legacy: hex(SHA-256(salt + ":" + password)). Compare in constant time
	// so response timing doesn't leak how many characters matched.
	sum := sha256.Sum256([]byte(s.LegacyPasswordSalt + ":" + password))
	return subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(stored)) == 1, true
}

// dummyHash is compared against when a login names an unknown user, so a
// miss costs the same bcrypt time as a wrong password and response times
// don't reveal which usernames exist.
func (s *Server) dummyHash() []byte {
	s.dummyOnce.Do(func() {
		s.dummy, _ = bcrypt.GenerateFromPassword([]byte("not-a-real-password"), s.BcryptCost)
	})
	return s.dummy
}

// ---- sessions ------------------------------------------------------------

// hashToken returns HMAC-SHA256(secret, token). Only this goes in the
// database: a leaked sessions table can't be turned back into cookies
// without also stealing SESSION_SECRET.
func (s *Server) hashToken(token string) string {
	mac := hmac.New(sha256.New, s.SessionSecret)
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

// startSession creates a session for userID and sets the cookie.
func (s *Server) startSession(ctx context.Context, w http.ResponseWriter, userID int64) error {
	raw := make([]byte, 32) // 256 bits from the OS CSPRNG: unguessable
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	now := s.Now()
	expires := now.Add(s.SessionTTL)
	if err := s.Store.CreateSession(ctx, s.hashToken(token), userID, now, expires); err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(s.SessionTTL.Seconds()),
		HttpOnly: true,                 // JavaScript can't read it, so XSS can't steal it
		Secure:   s.CookieSecure,       // only sent over HTTPS
		SameSite: http.SameSiteLaxMode, // not sent on cross-site POSTs, which blunts CSRF
	})
	return nil
}

// clearSessionCookie tells the browser to delete the cookie.
func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// currentUser returns the logged-in user's ID, if the request carries a
// valid, unexpired session cookie.
func (s *Server) currentUser(r *http.Request) (int64, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return 0, false
	}
	userID, err := s.Store.SessionUser(r.Context(), s.hashToken(c.Value), s.Now())
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log(r).Warn("session lookup failed", "err", err)
		}
		return 0, false
	}
	return userID, true
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

// purgeExpiredSessions deletes expired sessions every hour until ctx ends.
func (s *Server) purgeExpiredSessions(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := s.Store.DeleteExpiredSessions(ctx, s.Now()); err != nil {
				s.Logger.Warn("purging expired sessions failed", "err", err)
			} else if n > 0 {
				s.Logger.Info("purged expired sessions", "count", n)
			}
		}
	}
}

// ---- handlers ------------------------------------------------------------

// userResponse is what the API reveals about the logged-in user.
type userResponse struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// handleSignup creates an account and logs the new user straight in.
func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, badRequest(codeBadRequest, "request body must be valid JSON"))
		return
	}
	in.Username, in.Email = strings.TrimSpace(in.Username), strings.TrimSpace(in.Email)

	switch {
	case !validUsername.MatchString(in.Username):
		writeError(w, badRequest(codeBadRequest, "username must be 3-32 letters, digits, '.', '_' or '-'"))
		return
	case !isEmail(in.Email):
		writeError(w, badRequest(codeBadRequest, "please enter a valid email address"))
		return
	case len(in.Password) < minPasswordLen || len(in.Password) > maxPasswordLen:
		writeError(w, badRequest(codeBadRequest, "password must be 8 to 72 characters"))
		return
	}

	hash, err := s.hashPassword(in.Password)
	if err != nil {
		s.internalError(w, r, "hash password", err)
		return
	}
	userID, err := s.Store.CreateUser(r.Context(), in.Username, strings.ToLower(in.Email), hash)
	if errors.Is(err, store.ErrConflict) {
		writeErr(w, http.StatusConflict, codeConflict, "that username or email is already registered")
		return
	}
	if err != nil {
		s.internalError(w, r, "signup", err)
		return
	}
	if err := s.startSession(r.Context(), w, userID); err != nil {
		s.internalError(w, r, "start session", err)
		return
	}
	writeJSON(w, http.StatusCreated, userResponse{ID: userID, Username: in.Username})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"` // username or email
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, badRequest(codeBadRequest, "request body must be valid JSON"))
		return
	}

	u, err := s.Store.UserByLogin(r.Context(), strings.TrimSpace(in.Username))
	if errors.Is(err, store.ErrNotFound) {
		// Burn the same bcrypt time as a real check (see dummyHash).
		_ = bcrypt.CompareHashAndPassword(s.dummyHash(), []byte(in.Password))
		writeErr(w, http.StatusUnauthorized, codeUnauthorized, "invalid username or password")
		return
	}
	if err != nil {
		s.internalError(w, r, "login lookup", err)
		return
	}

	ok, legacy := s.checkPassword(u.PasswordHash, in.Password)
	if !ok {
		// Same message for unknown user and wrong password, so the
		// response doesn't reveal which usernames exist.
		writeErr(w, http.StatusUnauthorized, codeUnauthorized, "invalid username or password")
		return
	}
	if legacy {
		// We have the plaintext right now, so this is the one chance to
		// move this account onto bcrypt transparently.
		if hash, err := s.hashPassword(in.Password); err == nil {
			if err := s.Store.UpdatePasswordHash(r.Context(), u.ID, hash); err != nil {
				s.log(r).Warn("upgrading legacy password hash failed", "err", err)
			}
		}
	}

	if err := s.startSession(r.Context(), w, u.ID); err != nil {
		s.internalError(w, r, "start session", err)
		return
	}
	writeJSON(w, http.StatusOK, userResponse{ID: u.ID, Username: u.Username})
}

// handleLogout deletes the server-side session (so the token is dead even
// if someone copied it) and clears the cookie. Logging out twice is fine.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		if err := s.Store.DeleteSession(r.Context(), s.hashToken(c.Value)); err != nil {
			s.internalError(w, r, "logout", err)
			return
		}
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// handleMe tells the frontend who is logged in. It is needed because the
// HttpOnly session cookie is invisible to JavaScript.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, userID int64) {
	u, err := s.Store.UserByID(r.Context(), userID)
	if err != nil {
		s.internalError(w, r, "load user", err)
		return
	}
	writeJSON(w, http.StatusOK, userResponse{ID: u.ID, Username: u.Username})
}

// isEmail is a pragmatic check: one address, no display name, has a domain.
func isEmail(s string) bool {
	if len(s) > 254 {
		return false
	}
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && strings.Contains(s[strings.LastIndex(s, "@")+1:], ".")
}
