package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

// TestServeDrainsInFlightRequests proves graceful shutdown: a request that
// is mid-flight when the stop signal arrives still completes successfully,
// serve only returns once it has, and new connections are then refused.
func TestServeDrainsInFlightRequests(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release // simulate slow work, e.g. waiting on NASA
		_, _ = io.WriteString(w, "done")
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0") // port 0: pick any free port
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + ln.Addr().String()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- serve(ctx, &http.Server{Handler: handler}, ln, logger) }()

	// Start a request and wait until the handler is running.
	type result struct {
		body string
		err  error
	}
	reqDone := make(chan result, 1)
	go func() {
		resp, err := http.Get(url)
		if err != nil {
			reqDone <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		reqDone <- result{body: string(b), err: err}
	}()
	<-started

	cancel() // the "SIGTERM"

	// serve must keep waiting while the request is still in flight.
	select {
	case err := <-serveDone:
		t.Fatalf("serve returned (%v) before the in-flight request finished", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release) // let the slow request finish

	if r := <-reqDone; r.err != nil || r.body != "done" {
		t.Fatalf("in-flight request: body=%q err=%v, want it to complete", r.body, r.err)
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("serve returned %v, want nil after a clean shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after the last request finished")
	}

	if _, err := http.Get(url); err == nil {
		t.Error("server still accepted connections after shutdown")
	}
}
