package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/magic-peach/NASA-APOD-Explorer/internal/apod"
	"github.com/magic-peach/NASA-APOD-Explorer/internal/llm"
	"github.com/magic-peach/NASA-APOD-Explorer/internal/store"
)

// handleAPOD proxies APOD queries: ?date=, ?start_date=&end_date=, ?count=,
// or nothing for today. Single-date answers are a JSON object; ranges and
// random counts are arrays (matching NASA's own response shapes).
func (s *Server) handleAPOD(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	params := r.URL.Query()
	q := apod.Query{
		Date:      params.Get("date"),
		StartDate: params.Get("start_date"),
		EndDate:   params.Get("end_date"),
	}
	if c := params.Get("count"); c != "" {
		n, err := strconv.Atoi(c)
		if err != nil {
			http.Error(w, "count must be a number", http.StatusBadRequest)
			return
		}
		q.Count = n
	}
	userID, loggedIn := s.currentUser(r)

	// A single past date never changes, so check the persistent SQLite copy
	// before calling NASA.
	if q.IsSingle() && q.Date != "" {
		if item, err := s.Store.CachedAPOD(ctx, q.Date); err == nil {
			s.recordHistory(r, userID, loggedIn, []apod.Item{item})
			writeJSON(w, http.StatusOK, item)
			return
		} else if !errors.Is(err, store.ErrNotFound) {
			s.Logger.Warn("apod db cache lookup failed", "err", err)
		}
	}

	items, err := s.APOD.Fetch(ctx, q)
	if err != nil || len(items) == 0 {
		s.Logger.Error("apod fetch failed", "query", q.Key(), "err", err)
		http.Error(w, "failed to fetch APOD", http.StatusBadGateway)
		return
	}
	if err := s.Store.SaveAPOD(ctx, items); err != nil {
		s.Logger.Warn("saving apod to db cache failed", "err", err)
	}
	s.recordHistory(r, userID, loggedIn, items)

	if q.IsSingle() {
		writeJSON(w, http.StatusOK, items[0])
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// recordHistory logs views for signed-in users. Failures are logged but do
// not fail the request: history is a nice-to-have.
func (s *Server) recordHistory(r *http.Request, userID int64, loggedIn bool, items []apod.Item) {
	if !loggedIn {
		return
	}
	if err := s.Store.AddHistory(r.Context(), userID, items); err != nil {
		s.Logger.Warn("recording history failed", "err", err)
	}
}

func (s *Server) handleSpaceFacts(w http.ResponseWriter, r *http.Request) {
	summary, err := s.Facts.Summary(r.Context())
	if err != nil {
		s.Logger.Error("space facts fetch failed", "err", err)
		http.Error(w, "failed to fetch space facts", http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleLLM(w http.ResponseWriter, r *http.Request) {
	var req llm.Request
	if err := decodeJSON(w, r, &req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Task) == "" {
		http.Error(w, "task is required", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.Assistant.Run(r.Context(), req))
}

func (s *Server) handleListFavorites(w http.ResponseWriter, r *http.Request, userID int64) {
	favs, err := s.Store.ListFavorites(r.Context(), userID)
	if err != nil {
		s.internalError(w, "list favorites", err)
		return
	}
	writeJSON(w, http.StatusOK, favs)
}

func (s *Server) handleAddFavorite(w http.ResponseWriter, r *http.Request, userID int64) {
	var in store.Favorite
	if err := decodeJSON(w, r, &in); err != nil || in.Date == "" {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if err := s.Store.AddFavorite(r.Context(), userID, in); err != nil {
		s.internalError(w, "add favorite", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request, userID int64) {
	entries, err := s.Store.ListHistory(r.Context(), userID)
	if err != nil {
		s.internalError(w, "list history", err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleListComments(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date == "" {
		http.Error(w, "missing date", http.StatusBadRequest)
		return
	}
	comments, err := s.Store.ListComments(r.Context(), date)
	if err != nil {
		s.internalError(w, "list comments", err)
		return
	}
	writeJSON(w, http.StatusOK, comments)
}

func (s *Server) handleAddComment(w http.ResponseWriter, r *http.Request, userID int64) {
	date := r.URL.Query().Get("date")
	if date == "" {
		http.Error(w, "missing date", http.StatusBadRequest)
		return
	}
	var in struct {
		Comment string `json:"comment"`
	}
	if err := decodeJSON(w, r, &in); err != nil || strings.TrimSpace(in.Comment) == "" {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if err := s.Store.AddComment(r.Context(), userID, date, in.Comment); err != nil {
		s.internalError(w, "add comment", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAverageRating(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date == "" {
		http.Error(w, "missing date", http.StatusBadRequest)
		return
	}
	avg, err := s.Store.AverageRating(r.Context(), date)
	if err != nil {
		s.internalError(w, "average rating", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"average": avg})
}

func (s *Server) handleSetRating(w http.ResponseWriter, r *http.Request, userID int64) {
	date := r.URL.Query().Get("date")
	if date == "" {
		http.Error(w, "missing date", http.StatusBadRequest)
		return
	}
	var in struct {
		Rating int `json:"rating"`
	}
	if err := decodeJSON(w, r, &in); err != nil || in.Rating < 1 || in.Rating > 5 {
		http.Error(w, "invalid rating", http.StatusBadRequest)
		return
	}
	if err := s.Store.SetRating(r.Context(), userID, date, in.Rating); err != nil {
		s.internalError(w, "set rating", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// internalError logs the real cause and returns a generic 500, so database
// details never leak to clients.
func (s *Server) internalError(w http.ResponseWriter, op string, err error) {
	s.Logger.Error(op+" failed", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
