package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// middleware wraps a handler with extra behaviour.
type middleware func(http.Handler) http.Handler

// chain applies middlewares so the first one listed is the outermost, i.e.
// chain(h, a, b) == a(b(h)), and a request passes through a, then b, then h.
func chain(h http.Handler, mws ...middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// ---- request ID ----------------------------------------------------------

type ctxKey int

const requestIDKey ctxKey = iota

// validRequestID limits IDs accepted from upstream proxies to a safe
// charset and length, so a client can't inject arbitrary text into logs.
var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// withRequestID gives each request an ID, stores it in the request context,
// and echoes it in the X-Request-ID response header. Correlating a user's
// error report with server logs then takes one grep.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !validRequestID.MatchString(id) {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// requestID returns the ID stored by withRequestID, or "".
func requestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// ---- structured logging --------------------------------------------------

// statusRecorder captures the status code and size a handler writes, which
// http.ResponseWriter does not otherwise expose.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (rec *statusRecorder) WriteHeader(code int) {
	if !rec.wroteHeader {
		rec.status, rec.wroteHeader = code, true
	}
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *statusRecorder) Write(b []byte) (int, error) {
	if !rec.wroteHeader {
		rec.WriteHeader(http.StatusOK) // mirrors net/http's implicit 200
	}
	n, err := rec.ResponseWriter.Write(b)
	rec.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer (for
// flushing, deadlines, etc.) through this wrapper.
func (rec *statusRecorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }

// withLogging writes one structured log line per request.
func withLogging(logger *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			level := slog.LevelInfo
			switch {
			case rec.status >= 500:
				level = slog.LevelError
			case r.URL.Path == "/healthz" || r.URL.Path == "/readyz":
				level = slog.LevelDebug // probes run every few seconds; keep them out of normal logs
			}
			logger.Log(r.Context(), level, "http request",
				"request_id", requestID(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

// ---- panic recovery ------------------------------------------------------

// withRecovery turns a panic in any handler into a logged 500 instead of a
// dropped connection. (net/http would also recover, but silently and
// without a response body.)
func withRecovery(logger *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v) // deliberate abort: let net/http handle it as designed
				}
				logger.Error("panic recovered",
					"request_id", requestID(r.Context()),
					"panic", v,
					"stack", string(debug.Stack()),
				)
				// Only write a response if the handler hadn't started one.
				if rec, ok := w.(*statusRecorder); !ok || !rec.wroteHeader {
					writeErr(w, http.StatusInternalServerError, codeInternal, "internal server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// ---- CORS ----------------------------------------------------------------

// withCORS allows cross-origin calls only from allowedOrigin. The browser
// sends an Origin header on cross-origin requests; if it isn't exactly the
// allowed origin we add no CORS headers, so the browser blocks the page
// from reading the response. With allowedOrigin empty, only same-origin
// pages (our own frontend) can use the API.
//
// This replaces the previous "Access-Control-Allow-Origin: *", which let
// any website read the API.
func withCORS(allowedOrigin string) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Responses differ by Origin, so caches must key on it.
			w.Header().Add("Vary", "Origin")

			origin := r.Header.Get("Origin")
			allowed := origin != "" && allowedOrigin != "" && origin == allowedOrigin
			if allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}

			// Preflight: the browser asks before a non-simple cross-origin call.
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				if !allowed {
					writeErr(w, http.StatusForbidden, codeForbidden, "origin not allowed")
					return
				}
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
				w.Header().Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---- security headers ---------------------------------------------------

// contentSecurityPolicy tells the browser what the page may load. The
// frontend uses no inline scripts or styles, so script-src can be 'self'
// only: even if an attacker injected HTML, the browser would refuse to run
// their script. Images and video come from NASA hosts over HTTPS; video
// embeds are limited to YouTube's privacy-enhanced domain and Vimeo.
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self'; " +
	"img-src 'self' https: data:; " +
	"media-src 'self' https:; " +
	"frame-src https://www.youtube-nocookie.com https://player.vimeo.com; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// withSecurityHeaders sets protective headers on every response.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")                                  // don't guess content types
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")                 // don't leak full URLs to other sites
		h.Set("X-Frame-Options", "DENY")                                            // legacy twin of frame-ancestors (clickjacking)
		h.Set("Permissions-Policy", "camera=(), geolocation=(), microphone=(self)") // microphone: voice questions
		next.ServeHTTP(w, r)
	})
}

// ---- CSRF origin check --------------------------------------------------

// withOriginCheck rejects state-changing requests (POST, PUT, PATCH,
// DELETE) whose Origin header names another site. Browsers attach Origin to
// these requests, so a forged form or fetch from evil.example is refused
// even before we look at the session cookie. This is defence in depth on
// top of SameSite=Lax cookies and the JSON-only request bodies.
func withOriginCheck(allowedOrigin string) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			origin := r.Header.Get("Origin")
			if origin == "" || sameHost(origin, r.Host) || (allowedOrigin != "" && origin == allowedOrigin) {
				// No Origin means a non-browser client (curl, tests): it
				// can't carry a victim's cookies, so CSRF doesn't apply.
				next.ServeHTTP(w, r)
				return
			}
			writeErr(w, http.StatusForbidden, codeForbidden, "cross-site request blocked")
		})
	}
}

// sameHost reports whether origin ("https://host:port") refers to host.
func sameHost(origin, host string) bool {
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, host)
}

// ---- per-IP rate limiting ------------------------------------------------

// ipRateLimiter keeps one token-bucket limiter per client IP. Each bucket
// refills at rps tokens per second up to burst; a request spends a token,
// and an empty bucket means 429 Too Many Requests.
type ipRateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	rps      rate.Limit
	burst    int
	now      func() time.Time
}

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newIPRateLimiter(rps float64, burst int) *ipRateLimiter {
	return &ipRateLimiter{
		visitors: make(map[string]*visitor),
		rps:      rate.Limit(rps),
		burst:    burst,
		now:      time.Now,
	}
}

// allow reports whether ip may make a request now.
func (l *ipRateLimiter) allow(ip string) bool {
	l.mu.Lock()
	v, ok := l.visitors[ip]
	if !ok {
		v = &visitor{limiter: rate.NewLimiter(l.rps, l.burst)}
		l.visitors[ip] = v
	}
	v.lastSeen = l.now()
	l.mu.Unlock()
	return v.limiter.Allow() // rate.Limiter is itself safe for concurrent use
}

// forgetIdle drops limiters unused for longer than maxIdle. Without this
// the map would grow by one entry per distinct client IP, forever.
func (l *ipRateLimiter) forgetIdle(maxIdle time.Duration) {
	cutoff := l.now().Add(-maxIdle)
	l.mu.Lock()
	defer l.mu.Unlock()
	for ip, v := range l.visitors {
		if v.lastSeen.Before(cutoff) {
			delete(l.visitors, ip)
		}
	}
}

// runCleanup calls forgetIdle every minute until ctx is cancelled.
func (l *ipRateLimiter) runCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.forgetIdle(10 * time.Minute)
		}
	}
}

// withRateLimit applies the limiter to /api/ routes. Static assets and
// health probes are exempt: they're cheap, and a probe being throttled
// would get a healthy instance restarted.
func withRateLimit(l *ipRateLimiter, ipFrom func(*http.Request) string) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") && !l.allow(ipFrom(r)) {
				// Retry-After is a hint in seconds; one token refills in 1/rps.
				w.Header().Set("Retry-After", strconv.Itoa(max(1, int(1/float64(l.rps)))))
				writeErr(w, http.StatusTooManyRequests, codeRateLimited, "too many requests, please slow down")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP returns a function that extracts the client's IP. If
// trustedHeader is set (e.g. "Fly-Client-IP") and present it is used,
// because behind a reverse proxy RemoteAddr is the proxy, not the user.
// Otherwise RemoteAddr is used; we never read X-Forwarded-For blindly, as
// any client can set it to dodge the rate limiter.
func clientIP(trustedHeader string) func(*http.Request) string {
	return func(r *http.Request) string {
		if trustedHeader != "" {
			if ip := strings.TrimSpace(r.Header.Get(trustedHeader)); net.ParseIP(ip) != nil {
				return ip
			}
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			return r.RemoteAddr
		}
		return host
	}
}
