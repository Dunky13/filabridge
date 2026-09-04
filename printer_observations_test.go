package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type countingPrusaLinkServer struct {
	server      *httptest.Server
	statusCalls atomic.Int32
	jobCalls    atomic.Int32
}

func newCompletingPrusaLinkServer(t *testing.T) *countingPrusaLinkServer {
	t.Helper()
	printer := &countingPrusaLinkServer{}
	printer.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/status":
			call := printer.statusCalls.Add(1)
			if call == 1 {
				_, _ = fmt.Fprint(w, `{"job":{"id":7,"progress":25},"printer":{"state":"PRINTING"}}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"job":{"id":7,"progress":100},"printer":{"state":"FINISHED"}}`)
		case "/api/v1/job":
			printer.jobCalls.Add(1)
			state := StatePrinting
			if printer.statusCalls.Load() > 1 {
				state = StateFinished
			}
			_, _ = fmt.Fprintf(w, `{"id":7,"state":%q,"progress":100,"file":{"name":"widget.bgcode","display_name":"Widget","path":"usb","meta":{"filament_used_g_per_tool":[4.5]}}}`, state)
		default:
			http.NotFound(w, request)
		}
	}))
	t.Cleanup(printer.server.Close)
	return printer
}

func newCountingPrusaLinkServer(t *testing.T) *countingPrusaLinkServer {
	t.Helper()
	printer := &countingPrusaLinkServer{}
	printer.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/status":
			printer.statusCalls.Add(1)
			_, _ = fmt.Fprint(w, `{"job":{"id":7,"progress":25,"time_remaining":90,"time_printing":30},"printer":{"state":"PRINTING"}}`)
		case "/api/v1/job":
			printer.jobCalls.Add(1)
			_, _ = fmt.Fprint(w, `{"id":7,"state":"PRINTING","progress":25,"time_remaining":90,"time_printing":30,"file":{"name":"widget.bgcode","display_name":"Widget","path":"usb","meta":{}}}`)
		default:
			http.NotFound(w, request)
		}
	}))
	t.Cleanup(printer.server.Close)
	return printer
}

func newObservationTestBridge(t *testing.T, printerURL string, pollInterval time.Duration) *FilamentBridge {
	t.Helper()
	spoolman := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/spool" {
			http.NotFound(w, request)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `[]`)
	}))
	t.Cleanup(spoolman.Close)
	config := &Config{
		DBFile:                       filepath.Join(t.TempDir(), "filabridge.db"),
		SpoolmanURL:                  spoolman.URL,
		SpoolmanTimeout:              1,
		PrusaLinkTimeout:             1,
		PrusaLinkFileDownloadTimeout: 1,
		PollInterval:                 pollInterval,
		Printers: map[string]PrinterConfig{
			"printer-stable-id": {
				Name:      "Printer A",
				Model:     ModelMK4,
				IPAddress: printerURL,
				Toolheads: 1,
			},
		},
	}
	bridge, err := NewFilamentBridge(config)
	if err != nil {
		t.Fatalf("NewFilamentBridge() error = %v", err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	if err := bridge.SavePrinterConfig("printer-stable-id", config.Printers["printer-stable-id"]); err != nil {
		t.Fatalf("SavePrinterConfig() error = %v", err)
	}
	return bridge
}

func TestMonitorCycleFeedsStatusWithoutDuplicatePrinterPolls(t *testing.T) {
	printer := newCountingPrusaLinkServer(t)
	bridge := newObservationTestBridge(t, printer.server.URL, time.Hour)

	bridge.MonitorPrinters()
	status, err := bridge.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus() error = %v", err)
	}
	web := NewWebServerForHost(bridge, "127.0.0.1")
	web.BroadcastStatus()
	web.Shutdown()

	// Give a legacy detached monitor goroutine enough time to expose a second poll.
	time.Sleep(100 * time.Millisecond)
	if got := printer.statusCalls.Load(); got != 1 {
		t.Fatalf("PrusaLink status calls = %d, want one call in one monitor cycle", got)
	}
	if got := printer.jobCalls.Load(); got != 1 {
		t.Fatalf("PrusaLink job calls = %d, want one call in one monitor cycle", got)
	}
	printerStatus, exists := status.Printers["printer-stable-id"]
	if !exists {
		t.Fatalf("status keys = %v, want stable printer ID", status.Printers)
	}
	if printerStatus.State != StatePrinting || printerStatus.CurrentJob != "Widget" {
		t.Fatalf("printer status = %+v, want cached PRINTING observation for Widget", printerStatus)
	}
}

func TestMonitorLoopWakesImmediatelyWhenPollIntervalChanges(t *testing.T) {
	printer := newCountingPrusaLinkServer(t)
	bridge := newObservationTestBridge(t, printer.server.URL, time.Hour)
	runtime := &ApplicationRuntime{bridge: bridge}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.monitorLoop(ctx, func() {})
	}()

	waitForStatusCalls(t, &printer.statusCalls, 1, time.Second)
	updated := bridge.GetConfigSnapshot()
	updated.PollInterval = 10 * time.Millisecond
	if err := bridge.UpdateConfig(updated); err != nil {
		t.Fatalf("UpdateConfig() error = %v", err)
	}
	waitForStatusCalls(t, &printer.statusCalls, 2, 500*time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor loop did not stop")
	}
}

func TestCachedPrinterStatusSupportsConcurrentReadsDuringWebShutdown(t *testing.T) {
	printer := newCountingPrusaLinkServer(t)
	bridge := newObservationTestBridge(t, printer.server.URL, time.Hour)
	bridge.MonitorPrinters()
	web := NewWebServerForHost(bridge, "127.0.0.1")

	const readers = 12
	start := make(chan struct{})
	errors := make(chan error, readers)
	var reads sync.WaitGroup
	for range readers {
		reads.Add(1)
		go func() {
			defer reads.Done()
			<-start
			for range 20 {
				status, err := bridge.GetStatus()
				if err != nil {
					errors <- err
					return
				}
				if status.Printers["printer-stable-id"].State != StatePrinting {
					errors <- fmt.Errorf("cached state = %q, want %q", status.Printers["printer-stable-id"].State, StatePrinting)
					return
				}
			}
		}()
	}
	close(start)
	web.Shutdown()
	web.Shutdown()
	reads.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	if got := printer.statusCalls.Load(); got != 1 {
		t.Fatalf("PrusaLink status calls after concurrent cached reads = %d, want 1", got)
	}
}

func TestWebOnlyRuntimeRefreshesObservationsWithoutReconciliationOrDebit(t *testing.T) {
	printer := newCompletingPrusaLinkServer(t)
	spoolman := newHistoryTestSpoolmanServer()
	t.Cleanup(spoolman.close)
	config := &Config{
		DBFile:                       filepath.Join(t.TempDir(), "filabridge.db"),
		SpoolmanURL:                  spoolman.server.URL,
		SpoolmanTimeout:              1,
		PrusaLinkTimeout:             1,
		PrusaLinkFileDownloadTimeout: 1,
		PollInterval:                 10 * time.Millisecond,
		Printers: map[string]PrinterConfig{
			"printer-stable-id": {Name: "Printer A", Model: ModelMK4, IPAddress: printer.server.URL, Toolheads: 1},
		},
	}
	bridge, err := NewFilamentBridge(config)
	if err != nil {
		t.Fatalf("NewFilamentBridge() error = %v", err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	if err := bridge.SavePrinterConfig("printer-stable-id", config.Printers["printer-stable-id"]); err != nil {
		t.Fatalf("SavePrinterConfig() error = %v", err)
	}
	if err := bridge.setToolheadMappingRecord("printer-stable-id", 0, 20); err != nil {
		t.Fatalf("setToolheadMappingRecord() error = %v", err)
	}

	web := NewWebServerForHost(bridge, "127.0.0.1")
	runtime, err := NewApplicationRuntime(bridge, web, RuntimeOptions{Mode: RuntimeModeWebOnly, Host: "127.0.0.1", Port: "0"})
	if err != nil {
		t.Fatalf("NewApplicationRuntime() error = %v", err)
	}
	server := newFakeRuntimeHTTPServer()
	runtime.newHTTPServer = func(string, http.Handler) runtimeHTTPServer { return server }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	waitForStatusCalls(t, &printer.statusCalls, 2, time.Second)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("web-only runtime did not stop")
	}

	status, err := bridge.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus() error = %v", err)
	}
	if got := status.Printers["printer-stable-id"].State; got != StateFinished {
		t.Fatalf("cached web-only state = %q, want %q", got, StateFinished)
	}
	var checkpoints int
	if err := bridge.db.QueryRow("SELECT COUNT(*) FROM printer_job_checkpoints").Scan(&checkpoints); err != nil {
		t.Fatalf("count printer checkpoints: %v", err)
	}
	if checkpoints != 0 {
		t.Fatalf("web-only reconciliation checkpoints = %d, want 0", checkpoints)
	}
	if history, err := bridge.GetPrintHistory(10); err != nil || len(history) != 0 {
		t.Fatalf("web-only print history = %+v, err=%v; want no accounting", history, err)
	}
	if got := spoolman.usedWeight(20); got != 5 {
		t.Fatalf("web-only spool used weight = %.2f, want unchanged 5.00", got)
	}
}

func TestGetStatusKeepsDuplicatePrinterNamesIsolatedByStableID(t *testing.T) {
	printer := newCountingPrusaLinkServer(t)
	bridge := newObservationTestBridge(t, printer.server.URL, time.Hour)
	config := bridge.GetConfigSnapshot()
	first := config.Printers["printer-stable-id"]
	first.Name = "Workshop Printer"
	first.Toolheads = 2
	second := first
	second.IPAddress = "second-printer.invalid"
	config.Printers["printer-stable-id"] = first
	config.Printers["second-stable-id"] = second
	if err := bridge.SavePrinterConfig("printer-stable-id", first); err != nil {
		t.Fatal(err)
	}
	if err := bridge.SavePrinterConfig("second-stable-id", second); err != nil {
		t.Fatal(err)
	}
	if err := bridge.UpdateConfig(config); err != nil {
		t.Fatal(err)
	}
	if err := bridge.setToolheadMappingRecord("printer-stable-id", 0, 10); err != nil {
		t.Fatal(err)
	}
	if err := bridge.setToolheadMappingRecord("second-stable-id", 0, 20); err != nil {
		t.Fatal(err)
	}

	status, err := bridge.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus() error = %v", err)
	}
	if got := status.ToolheadMappings["printer-stable-id"][0].SpoolID; got != 10 {
		t.Fatalf("first printer spool = %d, want 10", got)
	}
	if got := status.ToolheadMappings["second-stable-id"][0].SpoolID; got != 20 {
		t.Fatalf("second printer spool = %d, want 20", got)
	}
	unmapped := status.ToolheadMappings["printer-stable-id"][1]
	if unmapped.PrinterID != "printer-stable-id" {
		t.Fatalf("synthesized unmapped printer ID = %q, want stable ID", unmapped.PrinterID)
	}
}

func waitForStatusCalls(t *testing.T, calls *atomic.Int32, want int32, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if calls.Load() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("PrusaLink status calls = %d, want at least %d within %s", calls.Load(), want, timeout)
}
