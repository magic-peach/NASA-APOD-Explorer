package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// errorBody decodes the standard {"error": {...}} shape.
func errorBody(t *testing.T, rec *httptest.ResponseRecorder) apiError {
	t.Helper()
	var body struct {
		Error apiError `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("response is not the JSON error shape: %v", err)
	}
	return body.Error
}

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func TestRequestID(t *testing.T) {
	tests := []struct {
		name     string
		incoming string
		wantSame bool
	}{
		{name: "generated when absent", incoming: "", wantSame: false},
		{name: "valid incoming ID is kept", incoming: "abc-123", wantSame: true},
		{name: "unsafe incoming ID is replaced", incoming: "bad id\nwith newline", wantSame: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen string
			h := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = requestID(r.Context())
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.incoming != "" {
				req.Header.Set("X-Request-ID", tt.incoming)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if seen == "" || rec.Header().Get("X-Request-ID") != seen {
				t.Fatalf("context ID %q, header %q", seen, rec.Header().Get("X-Request-ID"))
			}
			if (seen == tt.incoming) != tt.wantSame {
				t.Errorf("ID = %q, incoming %q, wantSame=%v", seen, tt.incoming, tt.wantSame)
			}
		})
	}
}

func TestRecoveryReturnsJSON500(t *testing.T) {
	h := withRecovery(discardLogger())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if e := errorBody(t, rec); e.Code != codeInternal {
		t.Errorf("code = %q", e.Code)
	}
}

func TestCORS(t *testing.T) {
	const allowed = "https://apod.example.com"
	h := withCORS(allowed)(okHandler)

	tests := []struct {
		name       string
		method     string
		origin     string
		preflight  bool
		wantStatus int
		wantACAO   string
	}{
		{name: "same-origin request (no Origin header)", method: "GET", wantStatus: 200},
		{name: "allowed origin", method: "GET", origin: allowed, wantStatus: 200, wantACAO: allowed},
		{name: "other origin gets no CORS header", method: "GET", origin: "https://evil.example", wantStatus: 200},
		{name: "preflight from allowed origin", method: "OPTIONS", origin: allowed, preflight: true, wantStatus: 204, wantACAO: allowed},
		{name: "preflight from other origin", method: "OPTIONS", origin: "https://evil.example", preflight: true, wantStatus: 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/apod", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.preflight {
				req.Header.Set("Access-Control-Request-Method", "POST")
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tt.wantACAO {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, tt.wantACAO)
			}
		})
	}
}

func TestRateLimitPerIP(t *testing.T) {
	limiter := newIPRateLimiter(1, 3) // burst of 3, then 1 per second
	h := withRateLimit(limiter, clientIP(""))(okHandler)

	do := func(path, remote string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := range 3 {
		if code := do("/api/apod", "1.1.1.1:1000"); code != 200 {
			t.Fatalf("request %d within burst: status %d", i, code)
		}
	}
	if code := do("/api/apod", "1.1.1.1:2000"); code != http.StatusTooManyRequests {
		t.Fatalf("4th request: status %d, want 429 (same IP, different port)", code)
	}
	if code := do("/api/apod", "2.2.2.2:1000"); code != 200 {
		t.Errorf("a different IP was throttled: %d", code)
	}
	if code := do("/healthz", "1.1.1.1:1000"); code != 200 {
		t.Errorf("health probe was throttled: %d", code)
	}
}

func TestClientIPOnlyTrustsConfiguredHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:5555"
	req.Header.Set("X-Forwarded-For", "6.6.6.6")
	req.Header.Set("Fly-Client-IP", "8.8.8.8")

	if got := clientIP("")(req); got != "10.0.0.1" {
		t.Errorf("no trusted header: got %q, want RemoteAddr host", got)
	}
	if got := clientIP("Fly-Client-IP")(req); got != "8.8.8.8" {
		t.Errorf("trusted header: got %q", got)
	}
}

func TestLimiterForgetsIdleVisitors(t *testing.T) {
	l := newIPRateLimiter(1, 1)
	now := time.Now()
	l.now = func() time.Time { return now }
	l.allow("1.1.1.1")

	now = now.Add(11 * time.Minute)
	l.forgetIdle(10 * time.Minute)
	if len(l.visitors) != 0 {
		t.Errorf("idle visitor not removed")
	}
}

func TestSecurityHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	withSecurityHeaders(okHandler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"script-src 'self'", "object-src 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q is missing %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP must not allow inline or eval'd script: %q", csp)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing nosniff")
	}
}
