package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/magic-peach/NASA-APOD-Explorer/internal/apod"
	"github.com/magic-peach/NASA-APOD-Explorer/internal/llm"
	"github.com/magic-peach/NASA-APOD-Explorer/internal/store"
)

// handleAPOD proxies APOD queries: ?date=, ?start_date=&end_date=, ?count=,
// or nothing for today. Single-date answers are a JSON object; ranges and
// random counts are arrays (matching NASA's own response shapes).
func (s *Server) handleAPOD(w http.ResponseWriter, r *http.Request) {
	q, apiErr := parseAPODQuery(r.URL.Query(), s.Now())
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}

	// Derive from r.Context() so that if the client disconnects, or the
	// server is shutting down, the upstream call is abandoned too; the
	// timeout stops one slow upstream from tying up the handler forever.
	ctx, cancel := context.WithTimeout(r.Context(), upstreamTimeout)
	defer cancel()

	var items []apod.Item
	if q.Date != "" {
		item, err := s.fetchDate(ctx, q.Date)
		if err != nil {
			s.upstreamError(w, r, "NASA APOD", err)
			return
		}
		items = []apod.Item{item}
	} else {
		var err error
		items, err = s.APOD.Fetch(ctx, q)
		if err == nil && len(items) == 0 {
			err = errors.New("empty response")
		}
		if err != nil {
			s.upstreamError(w, r, "NASA APOD", err)
			return
		}
		if err := s.Store.SaveAPOD(ctx, items); err != nil {
			s.log(r).Warn("saving apod to db cache failed", "err", err)
		}
	}

	if userID, ok := s.currentUser(r); ok {
		if err := s.Store.AddHistory(ctx, userID, items); err != nil {
			s.log(r).Warn("recording history failed", "err", err) // history is best-effort
		}
	}

	if q.IsSingle() {
		writeJSON(w, http.StatusOK, items[0])
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// fetchDate returns the entry for one date, preferring the SQLite copy: a
// past APOD never changes, so it only ever needs fetching once.
func (s *Server) fetchDate(ctx context.Context, date string) (apod.Item, error) {
	item, err := s.Store.CachedAPOD(ctx, date)
	if err == nil {
		return item, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		s.Logger.Warn("apod db cache lookup failed", "err", err)
	}

	items, err := s.APOD.Fetch(ctx, apod.Query{Date: date})
	if err != nil {
		return apod.Item{}, err
	}
	if len(items) == 0 {
		return apod.Item{}, errors.New("empty response")
	}
	if err := s.Store.SaveAPOD(ctx, items); err != nil {
		s.Logger.Warn("saving apod to db cache failed", "err", err)
	}
	return items[0], nil
}

const (
	defaultOnThisDayYears = 5
	maxOnThisDayYears     = 10
)

// handleOnThisDay returns the APOD for the same calendar day in each of the
// previous N years: GET /api/apod/on-this-day?date=YYYY-MM-DD&years=5.
//
// It needs N independent upstream calls, so they run concurrently with an
// errgroup. SetLimit caps how many are in flight at once so one request
// can't fire ten simultaneous calls at NASA.
//
// We use a plain errgroup.Group rather than errgroup.WithContext: WithContext
// cancels every call as soon as one fails (fail-fast), but for a "nice to
// have" strip a missing year should just be skipped, not sink the rest.
func (s *Server) handleOnThisDay(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	anchor := apodToday(now)
	if d := r.URL.Query().Get("date"); d != "" {
		var apiErr *apiError
		if anchor, apiErr = parseAPODDate("date", d, now); apiErr != nil {
			writeError(w, apiErr)
			return
		}
	}
	years := defaultOnThisDayYears
	if y := r.URL.Query().Get("years"); y != "" {
		n, err := strconv.Atoi(y)
		if err != nil || n < 1 || n > maxOnThisDayYears {
			writeError(w, badRequest(codeBadRequest, "years must be a whole number from 1 to 10"))
			return
		}
		years = n
	}

	// Work out the dates first. AddDate normalises 29 Feb in a non-leap year
	// to 1 Mar; that isn't "the same day", so such years are skipped.
	var dates []string
	for k := 1; k <= years; k++ {
		d := anchor.AddDate(-k, 0, 0)
		if d.Day() != anchor.Day() || d.Before(firstAPODDate) {
			continue
		}
		dates = append(dates, d.Format(dateLayout))
	}

	ctx, cancel := context.WithTimeout(r.Context(), upstreamTimeout)
	defer cancel()

	// Each goroutine writes only its own slot, so results keeps date order
	// and needs no mutex.
	results := make([]*apod.Item, len(dates))
	var g errgroup.Group
	g.SetLimit(4)
	for i, date := range dates {
		g.Go(func() error {
			item, err := s.fetchDate(ctx, date)
			if err != nil {
				s.log(r).Warn("on-this-day fetch failed", "date", date, "err", err)
				return nil // best effort: skip this year
			}
			results[i] = &item
			return nil
		})
	}
	_ = g.Wait() // goroutines never return errors (see above)

	items := []apod.Item{}
	for _, it := range results {
		if it != nil {
			items = append(items, *it)
		}
	}
	if len(items) == 0 && len(dates) > 0 {
		writeErr(w, http.StatusBadGateway, codeUpstream, "could not reach NASA, please try again shortly")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleSpaceFacts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), upstreamTimeout)
	defer cancel()
	summary, err := s.Facts.Summary(ctx)
	if err != nil {
		s.upstreamError(w, r, "space facts", err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleLLM(w http.ResponseWriter, r *http.Request) {
	var req llm.Request
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, badRequest(codeBadRequest, "request body must be valid JSON"))
		return
	}
	if strings.TrimSpace(req.Task) == "" {
		writeError(w, badRequest(codeBadRequest, "task is required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), upstreamTimeout)
	defer cancel()
	writeJSON(w, http.StatusOK, s.Assistant.Run(ctx, req))
}

func (s *Server) handleListFavorites(w http.ResponseWriter, r *http.Request, userID int64) {
	favs, err := s.Store.ListFavorites(r.Context(), userID)
	if err != nil {
		s.internalError(w, r, "list favorites", err)
		return
	}
	writeJSON(w, http.StatusOK, favs)
}

func (s *Server) handleAddFavorite(w http.ResponseWriter, r *http.Request, userID int64) {
	var in store.Favorite
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, badRequest(codeBadRequest, "request body must be valid JSON"))
		return
	}
	if _, apiErr := parseAPODDate("date", in.Date, s.Now()); apiErr != nil {
		writeError(w, apiErr)
		return
	}
	if err := s.Store.AddFavorite(r.Context(), userID, in); err != nil {
		s.internalError(w, r, "add favorite", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteFavorite removes one favorite: DELETE /api/favorites/{date}.
func (s *Server) handleDeleteFavorite(w http.ResponseWriter, r *http.Request, userID int64) {
	date := r.PathValue("date") // Go 1.22 path wildcard
	if _, apiErr := parseAPODDate("date", date, s.Now()); apiErr != nil {
		writeError(w, apiErr)
		return
	}
	err := s.Store.DeleteFavorite(r.Context(), userID, date)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, codeNotFound, "that date is not in your favorites")
		return
	}
	if err != nil {
		s.internalError(w, r, "delete favorite", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request, userID int64) {
	entries, err := s.Store.ListHistory(r.Context(), userID)
	if err != nil {
		s.internalError(w, r, "list history", err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// dateParam reads and validates the ?date= parameter used by the comment
// and rating endpoints. ok is false if an error response was written.
func (s *Server) dateParam(w http.ResponseWriter, r *http.Request) (date string, ok bool) {
	date = r.URL.Query().Get("date")
	if _, apiErr := parseAPODDate("date", date, s.Now()); apiErr != nil {
		writeError(w, apiErr)
		return "", false
	}
	return date, true
}

const maxCommentLength = 2000

func (s *Server) handleListComments(w http.ResponseWriter, r *http.Request) {
	date, ok := s.dateParam(w, r)
	if !ok {
		return
	}
	comments, err := s.Store.ListComments(r.Context(), date)
	if err != nil {
		s.internalError(w, r, "list comments", err)
		return
	}
	writeJSON(w, http.StatusOK, comments)
}

func (s *Server) handleAddComment(w http.ResponseWriter, r *http.Request, userID int64) {
	date, ok := s.dateParam(w, r)
	if !ok {
		return
	}
	var in struct {
		Comment string `json:"comment"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, badRequest(codeBadRequest, "request body must be valid JSON"))
		return
	}
	in.Comment = strings.TrimSpace(in.Comment)
	if in.Comment == "" || len([]rune(in.Comment)) > maxCommentLength {
		writeError(w, badRequest(codeBadRequest, "comment must be 1 to 2000 characters"))
		return
	}
	if err := s.Store.AddComment(r.Context(), userID, date, in.Comment); err != nil {
		s.internalError(w, r, "add comment", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAverageRating(w http.ResponseWriter, r *http.Request) {
	date, ok := s.dateParam(w, r)
	if !ok {
		return
	}
	avg, err := s.Store.AverageRating(r.Context(), date)
	if err != nil {
		s.internalError(w, r, "average rating", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"average": avg})
}

func (s *Server) handleSetRating(w http.ResponseWriter, r *http.Request, userID int64) {
	date, ok := s.dateParam(w, r)
	if !ok {
		return
	}
	var in struct {
		Rating int `json:"rating"`
	}
	if err := decodeJSON(w, r, &in); err != nil || in.Rating < 1 || in.Rating > 5 {
		writeError(w, badRequest(codeBadRequest, "rating must be a whole number from 1 to 5"))
		return
	}
	if err := s.Store.SetRating(r.Context(), userID, date, in.Rating); err != nil {
		s.internalError(w, r, "set rating", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// upstreamError logs an upstream failure and returns 502 (or 504 when the
// upstream was simply too slow).
func (s *Server) upstreamError(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.log(r).Error(what+" upstream failed", "err", err)
	if errors.Is(err, context.DeadlineExceeded) {
		writeErr(w, http.StatusGatewayTimeout, codeUpstream, what+" took too long to respond, please try again")
		return
	}
	writeErr(w, http.StatusBadGateway, codeUpstream, "could not reach "+what+", please try again shortly")
}

// internalError logs the real cause and returns a generic 500, so database
// details never leak to clients.
func (s *Server) internalError(w http.ResponseWriter, r *http.Request, op string, err error) {
	s.log(r).Error(op+" failed", "err", err)
	writeErr(w, http.StatusInternalServerError, codeInternal, "something went wrong on our side")
}
