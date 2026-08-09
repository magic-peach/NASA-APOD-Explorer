package apod

import (
	"context"
	"testing"
	"time"
)

func TestDemoClient(t *testing.T) {
	d := &DemoClient{Now: func() time.Time { return time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC) }}
	ctx := context.Background()

	tests := []struct {
		name      string
		q         Query
		wantItems int
	}{
		{"today", Query{}, 1},
		{"one date", Query{Date: "2024-01-01"}, 1},
		{"range is inclusive", Query{StartDate: "2024-01-01", EndDate: "2024-01-07"}, 7},
		{"random count", Query{Count: 5}, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items, err := d.Fetch(ctx, tt.q)
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != tt.wantItems {
				t.Fatalf("got %d items, want %d", len(items), tt.wantItems)
			}
			for _, it := range items {
				if !it.Sample || it.Title == "" || it.URL == "" || it.Copyright == "" {
					t.Errorf("incomplete sample item: %+v", it)
				}
				if it.Date < "1995-06-16" || it.Date > "2024-06-01" {
					t.Errorf("date %s outside the archive", it.Date)
				}
			}
		})
	}

	// The same date always maps to the same sample (stable deep links).
	a, _ := d.Fetch(ctx, Query{Date: "2020-05-05"})
	b, _ := d.Fetch(ctx, Query{Date: "2020-05-05"})
	if a[0] != b[0] {
		t.Error("same date returned different samples")
	}
	// Today comes back dated today.
	today, _ := d.Fetch(ctx, Query{})
	if today[0].Date != "2024-06-01" {
		t.Errorf("today = %s", today[0].Date)
	}
}

func TestDemoSetIncludesAVideo(t *testing.T) {
	for _, e := range demoEntries {
		if e.mediaType == "video" && e.thumbnail != "" {
			return
		}
	}
	t.Error("the demo set should include a video with a thumbnail, to exercise video rendering")
}
