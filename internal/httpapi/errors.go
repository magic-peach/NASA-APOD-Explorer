package httpapi

import "net/http"

// Machine-readable error codes. Clients should branch on Code, never on the
// human-readable Message, which may change wording at any time.
const (
	codeBadRequest      = "bad_request"
	codeInvalidDate     = "invalid_date"
	codeDateOutOfRange  = "date_out_of_range"
	codeRangeTooLarge   = "range_too_large"
	codeInvalidCount    = "invalid_count"
	codeConflictingArgs = "conflicting_parameters"
	codeUnauthorized    = "unauthorized"
	codeConflict        = "conflict"
	codeNotFound        = "not_found"
	codeRateLimited     = "rate_limited"
	codeUpstream        = "upstream_unavailable"
	codeInternal        = "internal_error"
	codeForbidden       = "forbidden"
)

// apiError is the single error shape every endpoint returns:
//
//	{"error": {"code": "invalid_date", "message": "date must be YYYY-MM-DD"}}
type apiError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

// badRequest builds a 400 apiError.
func badRequest(code, message string) *apiError {
	return &apiError{Status: http.StatusBadRequest, Code: code, Message: message}
}

// writeError sends err as JSON with its HTTP status.
func writeError(w http.ResponseWriter, err *apiError) {
	writeJSON(w, err.Status, map[string]*apiError{"error": err})
}

// writeErr is a shorthand for one-off errors.
func writeErr(w http.ResponseWriter, status int, code, message string) {
	writeError(w, &apiError{Status: status, Code: code, Message: message})
}
