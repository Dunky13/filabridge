package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestConcurrentConfigReloadDashboardAndAccounting exercises three public
// seams that share live runtime configuration: reload, an HTTP request, and
// completed-print accounting. Run with -race to verify snapshot publication.
func TestConcurrentConfigReloadDashboardAndAccounting(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	t.Cleanup(spoolman.close)

	bridge := newTestBridge(t, spoolman.server.URL)
	if err := bridge.SetConfigValue(ConfigKeySpoolmanURL, spoolman.server.URL); err != nil {
		t.Fatalf("SetConfigValue(%s) error = %v", ConfigKeySpoolmanURL, err)
	}
	if err := bridge.SetToolheadMapping("printer-a", 0, 20); err != nil {
		t.Fatalf("SetToolheadMapping() error = %v", err)
	}
	server := NewWebServerForHost(bridge, "127.0.0.1")
	t.Cleanup(server.Shutdown)

	const iterations = 12
	start := make(chan struct{})
	errors := make(chan error, 3)
	var workers sync.WaitGroup
	workers.Add(3)

	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < iterations; i++ {
			if err := bridge.SetConfigValue(ConfigKeySpoolmanUsername, fmt.Sprintf("operator-%d", i)); err != nil {
				errors <- fmt.Errorf("SetConfigValue(%d): %w", i, err)
				return
			}
			if err := bridge.ReloadConfig(); err != nil {
				errors <- fmt.Errorf("ReloadConfig(%d): %w", i, err)
				return
			}
		}
	}()

	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < iterations; i++ {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			server.router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				errors <- fmt.Errorf("GET / (%d) status = %d", i, recorder.Code)
				return
			}
		}
	}()

	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < iterations; i++ {
			if err := bridge.AccountCompletedPrint(CompletedPrintObservation{
				PrinterID:    "printer-a",
				JobName:      fmt.Sprintf("race-%d.gcode", i),
				SourcePath:   fmt.Sprintf("usb/race-%d.gcode", i),
				FilamentUsed: map[int]float64{0: 0.25},
				StartedAt:    time.Unix(int64(i+1), 0).UTC(),
				PrintState:   StateFinished,
			}); err != nil {
				errors <- fmt.Errorf("AccountCompletedPrint(%d): %w", i, err)
				return
			}
		}
	}()

	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}
