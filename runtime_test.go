package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type fakeRuntimeHTTPServer struct {
	started  chan struct{}
	stopped  chan struct{}
	stopOnce sync.Once
}

func newFakeRuntimeHTTPServer() *fakeRuntimeHTTPServer {
	return &fakeRuntimeHTTPServer{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

func (s *fakeRuntimeHTTPServer) ListenAndServe() error {
	close(s.started)
	<-s.stopped
	return http.ErrServerClosed
}

func (s *fakeRuntimeHTTPServer) Shutdown(context.Context) error {
	s.stopOnce.Do(func() { close(s.stopped) })
	return nil
}

func TestRuntimeOptionsListenAddressHonorsHost(t *testing.T) {
	options := RuntimeOptions{Host: "127.0.0.1", Port: "5050"}
	if got := options.ListenAddress(); got != "127.0.0.1:5050" {
		t.Fatalf("ListenAddress() = %q, want 127.0.0.1:5050", got)
	}
}

func TestApplicationRuntimeCancellationStopsAllWorkers(t *testing.T) {
	server := newFakeRuntimeHTTPServer()
	cleanupStopped := make(chan struct{})
	monitorStopped := make(chan struct{})
	hub := newWebSocketHub()
	runtime := &ApplicationRuntime{
		options: RuntimeOptions{Mode: RuntimeModeCombined, Host: "127.0.0.1", Port: "0"},
		web:     &WebServer{router: gin.New(), wsHub: hub},
		newHTTPServer: func(string, http.Handler) runtimeHTTPServer {
			return server
		},
		runCleanup: func(ctx context.Context) {
			<-ctx.Done()
			close(cleanupStopped)
		},
		runMonitor: func(ctx context.Context, _ func()) {
			<-ctx.Done()
			close(monitorStopped)
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()

	select {
	case <-server.started:
	case <-time.After(time.Second):
		t.Fatal("HTTP server did not start")
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not return after cancellation")
	}

	for name, stopped := range map[string]<-chan struct{}{
		"cleanup":       cleanupStopped,
		"monitor":       monitorStopped,
		"HTTP":          server.stopped,
		"WebSocket hub": hub.done,
	} {
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatalf("%s worker did not stop", name)
		}
	}
}

func TestApplicationRuntimeReturnsHTTPFailure(t *testing.T) {
	wantErr := errors.New("listen failed")
	runtime := &ApplicationRuntime{
		options: RuntimeOptions{Mode: RuntimeModeWebOnly, Host: "127.0.0.1", Port: "0"},
		web:     &WebServer{router: gin.New()},
		newHTTPServer: func(string, http.Handler) runtimeHTTPServer {
			return failingRuntimeHTTPServer{err: wantErr}
		},
		runCleanup: func(ctx context.Context) { <-ctx.Done() },
		runObserve: func(ctx context.Context, _ func()) { <-ctx.Done() },
	}

	err := runtime.Run(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
}

type failingRuntimeHTTPServer struct{ err error }

func (s failingRuntimeHTTPServer) ListenAndServe() error          { return s.err }
func (s failingRuntimeHTTPServer) Shutdown(context.Context) error { return nil }
