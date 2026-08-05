package httpapi

import (
	"net/url"
	"testing"
	"time"

	"github.com/magic-peach/NASA-APOD-Explorer/internal/apod"
)

func TestParseAPODQuery(t *testing.T) {
	// 2024-03-10 03:00 UTC is still 2024-03-09 in New York, so "today" for
	// APOD purposes is the 9th. That lets us check the time-zone handling.
	now := time.Date(2024, 3, 10, 3, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		query    string
		want     apod.Query
		wantCode string // "" means no error expected
	}{
		{name: "no params means today", query: "", want: apod.Query{}},
		{name: "valid date", query: "date=2024-01-01", want: apod.Query{Date: "2024-01-01"}},
		{name: "first APOD day is allowed", query: "date=1995-06-16", want: apod.Query{Date: "1995-06-16"}},
		{name: "before APOD began", query: "date=1995-06-15", wantCode: codeDateOutOfRange},
		{name: "today in New York is allowed", query: "date=2024-03-09", want: apod.Query{Date: "2024-03-09"}},
		{name: "already tomorrow in New York terms", query: "date=2024-03-10", wantCode: codeDateOutOfRange},
		{name: "wrong format", query: "date=01/02/2024", wantCode: codeInvalidDate},
		{name: "unpadded", query: "date=2024-1-1", wantCode: codeInvalidDate},
		{name: "impossible day", query: "date=2023-02-29", wantCode: codeInvalidDate},
		{name: "injection attempt", query: "date=2024-01-01'--", wantCode: codeInvalidDate},

		{name: "valid range", query: "start_date=2024-01-01&end_date=2024-01-07",
			want: apod.Query{StartDate: "2024-01-01", EndDate: "2024-01-07"}},
		{name: "30-day range is the maximum", query: "start_date=2024-01-01&end_date=2024-01-30",
			want: apod.Query{StartDate: "2024-01-01", EndDate: "2024-01-30"}},
		{name: "31-day range is too large", query: "start_date=2024-01-01&end_date=2024-01-31", wantCode: codeRangeTooLarge},
		{name: "end before start", query: "start_date=2024-01-07&end_date=2024-01-01", wantCode: codeInvalidDate},
		{name: "only start", query: "start_date=2024-01-01", wantCode: codeBadRequest},
		{name: "range ending in the future", query: "start_date=2024-03-01&end_date=2024-03-12", wantCode: codeDateOutOfRange},

		{name: "count 1", query: "count=1", want: apod.Query{Count: 1}},
		{name: "count 10", query: "count=10", want: apod.Query{Count: 10}},
		{name: "count 0", query: "count=0", wantCode: codeInvalidCount},
		{name: "count 11", query: "count=11", wantCode: codeInvalidCount},
		{name: "count not a number", query: "count=lots", wantCode: codeInvalidCount},

		{name: "date with count", query: "date=2024-01-01&count=3", wantCode: codeConflictingArgs},
		{name: "date with range", query: "date=2024-01-01&start_date=2024-01-01&end_date=2024-01-02", wantCode: codeConflictingArgs},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatal(err)
			}
			got, apiErr := parseAPODQuery(params, now)

			if tt.wantCode != "" {
				if apiErr == nil {
					t.Fatalf("got %+v, want error %q", got, tt.wantCode)
				}
				if apiErr.Code != tt.wantCode || apiErr.Status != 400 {
					t.Fatalf("got error %s (status %d), want %s (400)", apiErr.Code, apiErr.Status, tt.wantCode)
				}
				return
			}
			if apiErr != nil {
				t.Fatalf("unexpected error: %v", apiErr)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
