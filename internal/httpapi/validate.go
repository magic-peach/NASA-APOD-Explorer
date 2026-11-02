package httpapi

import (
	"fmt"
	"net/url"
	"strconv"
	"time"

	_ "time/tzdata" // embed the time zone database: minimal container images have none

	"github.com/magic-peach/NASA-APOD-Explorer/internal/apod"
)

const (
	dateLayout   = "2006-01-02" // Go's reference date written as YYYY-MM-DD
	maxRangeDays = 30           // a range may return at most this many entries
	maxCount     = 10           // most random entries per request
)

// firstAPODDate is the day the APOD archive begins.
var firstAPODDate = time.Date(1995, time.June, 16, 0, 0, 0, 0, time.UTC)

// apodTimeZone is where NASA decides what "today" is. Using the server's
// zone instead would let a user in, say, India ask for "tomorrow" in US
// terms and get an upstream error rather than a clean 400.
var apodTimeZone = mustLoadLocation("America/New_York")

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err) // cannot happen: time/tzdata is embedded above
	}
	return loc
}

// apodToday returns today's APOD date (midnight UTC of the US Eastern date).
func apodToday(now time.Time) time.Time {
	y, m, d := now.In(apodTimeZone).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// parseAPODDate checks that s is a real YYYY-MM-DD date within the archive.
// name is the parameter name, used in error messages.
func parseAPODDate(name, s string, now time.Time) (time.Time, *apiError) {
	// time.Parse is strict: "2024-1-5" or "2024-02-30" are rejected.
	d, err := time.Parse(dateLayout, s)
	if err != nil {
		return time.Time{}, badRequest(codeInvalidDate, fmt.Sprintf("%s must be a valid date in YYYY-MM-DD format", name))
	}
	if d.Before(firstAPODDate) {
		return time.Time{}, badRequest(codeDateOutOfRange, fmt.Sprintf("%s must not be before %s, when APOD began", name, firstAPODDate.Format(dateLayout)))
	}
	if d.After(apodToday(now)) {
		return time.Time{}, badRequest(codeDateOutOfRange, fmt.Sprintf("%s must not be in the future", name))
	}
	return d, nil
}

// parseAPODQuery turns /api/apod query parameters into a validated
// apod.Query. Exactly one of these forms is accepted:
//
//	(nothing)                      today's entry
//	date=YYYY-MM-DD                one entry
//	start_date=...&end_date=...    up to 30 consecutive entries
//	count=N                        N random entries, 1 <= N <= 10
func parseAPODQuery(params url.Values, now time.Time) (apod.Query, *apiError) {
	date, start, end, count := params.Get("date"), params.Get("start_date"), params.Get("end_date"), params.Get("count")

	forms := 0
	for _, used := range []bool{date != "", start != "" || end != "", count != ""} {
		if used {
			forms++
		}
	}
	if forms > 1 {
		return apod.Query{}, badRequest(codeConflictingArgs, "use only one of date, start_date/end_date, or count")
	}

	switch {
	case date != "":
		if _, err := parseAPODDate("date", date, now); err != nil {
			return apod.Query{}, err
		}
		return apod.Query{Date: date}, nil

	case start != "" || end != "":
		if start == "" || end == "" {
			return apod.Query{}, badRequest(codeBadRequest, "start_date and end_date must be given together")
		}
		s, err := parseAPODDate("start_date", start, now)
		if err != nil {
			return apod.Query{}, err
		}
		e, err := parseAPODDate("end_date", end, now)
		if err != nil {
			return apod.Query{}, err
		}
		if e.Before(s) {
			return apod.Query{}, badRequest(codeInvalidDate, "end_date must not be before start_date")
		}
		// Both dates are inclusive, so the range holds days+1 entries.
		if days := int(e.Sub(s).Hours()/24) + 1; days > maxRangeDays {
			return apod.Query{}, badRequest(codeRangeTooLarge, fmt.Sprintf("a range may cover at most %d days (got %d)", maxRangeDays, days))
		}
		return apod.Query{StartDate: start, EndDate: end}, nil

	case count != "":
		n, err := strconv.Atoi(count)
		if err != nil || n < 1 || n > maxCount {
			return apod.Query{}, badRequest(codeInvalidCount, fmt.Sprintf("count must be a whole number from 1 to %d", maxCount))
		}
		return apod.Query{Count: n}, nil

	default:
		return apod.Query{}, nil // today
	}
}
