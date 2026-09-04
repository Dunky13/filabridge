package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// RuntimeMode selects which long-running workers the application owns.
type RuntimeMode string

const (
	RuntimeModeCombined   RuntimeMode = "combined"
	RuntimeModeWebOnly    RuntimeMode = "web-only"
	RuntimeModeBridgeOnly RuntimeMode = "bridge-only"
)

// RuntimeOptions contains process-level settings that are not persisted.
type RuntimeOptions struct {
	Mode RuntimeMode
	Host string
	Port string
}

// ListenAddress returns the exact address used by the HTTP server.
func (o RuntimeOptions) ListenAddress() string {
	return net.JoinHostPort(o.Host, o.Port)
}

type runtimeHTTPServer interface {
	ListenAndServe() error
	Shutdown(context.Context) error
}

// ApplicationRuntime owns cancellation, background workers, and HTTP shutdown.
type ApplicationRuntime struct {
	bridge  *FilamentBridge
	web     *WebServer
	options RuntimeOptions

	newHTTPServer func(string, http.Handler) runtimeHTTPServer
	runCleanup    func(context.Context)
	runMonitor    func(context.Context, func())
	runObserve    func(context.Context, func())
}

// NewApplicationRuntime creates the process runtime for one execution mode.
func NewApplicationRuntime(bridge *FilamentBridge, web *WebServer, options RuntimeOptions) (*ApplicationRuntime, error) {
	if bridge == nil {
		return nil, fmt.Errorf("bridge is required")
	}
	if options.Mode == "" {
		options.Mode = RuntimeModeCombined
	}
	if options.Mode != RuntimeModeCombined && options.Mode != RuntimeModeWebOnly && options.Mode != RuntimeModeBridgeOnly {
		return nil, fmt.Errorf("unsupported runtime mode %q", options.Mode)
	}
	if options.Host == "" {
		options.Host = "127.0.0.1"
	}
	if options.Port == "" {
		options.Port = DefaultWebPort
	}
	if options.Mode != RuntimeModeBridgeOnly && web == nil {
		return nil, fmt.Errorf("web server is required in %s mode", options.Mode)
	}

	runtime := &ApplicationRuntime{
		bridge:  bridge,
		web:     web,
		options: options,
		newHTTPServer: func(address string, handler http.Handler) runtimeHTTPServer {
			return &http.Server{
				Addr:              address,
				Handler:           handler,
				ReadHeaderTimeout: 10 * time.Second,
				ReadTimeout:       30 * time.Second,
				WriteTimeout:      2 * time.Minute,
				IdleTimeout:       2 * time.Minute,
			}
		},
	}
	runtime.runCleanup = runtime.cleanupLoop
	runtime.runMonitor = runtime.monitorLoop
	runtime.runObserve = runtime.observationLoop
	return runtime, nil
}

// Run starts selected workers and returns after cancellation or a fatal HTTP error.
func (r *ApplicationRuntime) Run(ctx context.Context) error {
	if r.newHTTPServer == nil {
		return fmt.Errorf("HTTP server factory is required")
	}
	if r.runCleanup == nil {
		return fmt.Errorf("cleanup worker is required")
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		r.runCleanup(runCtx)
	}()

	if r.options.Mode == RuntimeModeWebOnly {
		if r.runObserve == nil {
			cancel()
			workers.Wait()
			return fmt.Errorf("observation worker is required")
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			r.runObserve(runCtx, r.web.BroadcastStatus)
		}()
	} else {
		if r.runMonitor == nil {
			cancel()
			workers.Wait()
			return fmt.Errorf("monitor worker is required")
		}
		broadcast := func() {}
		if r.web != nil {
			broadcast = r.web.BroadcastStatus
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			r.runMonitor(runCtx, broadcast)
		}()
	}

	var server runtimeHTTPServer
	httpErrors := make(chan error, 1)
	if r.options.Mode != RuntimeModeBridgeOnly {
		server = r.newHTTPServer(r.options.ListenAddress(), r.web.router)
		workers.Add(1)
		go func() {
			defer workers.Done()
			err := server.ListenAndServe()
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				httpErrors <- err
			}
		}()
	}

	var runErr error
	if server == nil {
		<-runCtx.Done()
	} else {
		select {
		case <-runCtx.Done():
		case runErr = <-httpErrors:
		}
	}
	cancel()

	if server != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := server.Shutdown(shutdownCtx); err != nil && runErr == nil {
			runErr = fmt.Errorf("shutdown HTTP server: %w", err)
		}
		shutdownCancel()
	}
	if r.web != nil {
		r.web.Shutdown()
	}
	workers.Wait()
	return runErr
}

func (r *ApplicationRuntime) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.bridge.cleanupExpiredSessions(); err != nil {
				log.Printf("Error cleaning up NFC sessions: %v", err)
			}
		}
	}
}

func (r *ApplicationRuntime) monitorLoop(ctx context.Context, broadcast func()) {
	r.pollLoop(ctx, broadcast, r.bridge.MonitorPrinters)
}

func (r *ApplicationRuntime) observationLoop(ctx context.Context, broadcast func()) {
	r.pollLoop(ctx, broadcast, r.bridge.observePrinters)
}

func (r *ApplicationRuntime) pollLoop(ctx context.Context, broadcast func(), poll func()) {
	for {
		poll()
		broadcast()

		interval := DefaultPollInterval * time.Second
		if snapshot := r.bridge.GetConfigSnapshot(); snapshot != nil && snapshot.PollInterval > 0 {
			interval = snapshot.PollInterval
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-r.bridge.configChanges():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}
}
