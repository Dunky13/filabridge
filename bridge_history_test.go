package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func historyIntPointer(value int) *int {
	return &value
}

type historyTestSpoolmanServer struct {
	server    *httptest.Server
	mu        sync.Mutex
	locations []SpoolmanLocation
	spools    map[int]SpoolmanSpool
}

func newHistoryTestSpoolmanServer() *historyTestSpoolmanServer {
	ts := &historyTestSpoolmanServer{
		locations: []SpoolmanLocation{
			{ID: 1, Name: "Shelf"},
		},
		spools: map[int]SpoolmanSpool{
			10: {ID: 10, UsedWeight: 50, RemainingWeight: 250, Filament: &SpoolmanFilament{ColorHex: "ffffff"}, Name: "Old Spool", Brand: "Brand A", Material: "PLA"},
			20: {ID: 20, UsedWeight: 5, RemainingWeight: 300, Filament: &SpoolmanFilament{ColorHex: "000000"}, Name: "New Spool", Brand: "Brand B", Material: "PETG"},
		},
	}

	ts.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/location":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ts.locations)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/spool/"):
			spoolID, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/spool/"))
			if err != nil {
				http.Error(w, "bad spool id", http.StatusBadRequest)
				return
			}

			ts.mu.Lock()
			spool, ok := ts.spools[spoolID]
			ts.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(spool)
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/v1/spool/"):
			spoolID, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/spool/"))
			if err != nil {
				http.Error(w, "bad spool id", http.StatusBadRequest)
				return
			}

			var payload map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}

			ts.mu.Lock()
			spool, ok := ts.spools[spoolID]
			if ok {
				if usedWeight, exists := payload["used_weight"].(float64); exists {
					spool.UsedWeight = usedWeight
				}
				if location, exists := payload["location"].(string); exists {
					spool.Location = location
				}
				ts.spools[spoolID] = spool
			}
			ts.mu.Unlock()

			if !ok {
				http.NotFound(w, r)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))

	return ts
}

func (ts *historyTestSpoolmanServer) close() {
	ts.server.Close()
}

func (ts *historyTestSpoolmanServer) usedWeight(spoolID int) float64 {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.spools[spoolID].UsedWeight
}

func TestUpdatePrintHistorySpoolRebalancesUsage(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()

	bridge := newTestBridge(t, spoolman.server.URL)

	if err := bridge.LogPrintUsage("Printer A", 0, historyIntPointer(10), 12.5, "cube.gcode"); err != nil {
		t.Fatalf("LogPrintUsage() error = %v", err)
	}

	history, err := bridge.GetPrintHistory(10)
	if err != nil {
		t.Fatalf("GetPrintHistory() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history length = %d, want 1", len(history))
	}

	if err := bridge.UpdatePrintHistorySpool(history[0].ID, historyIntPointer(20)); err != nil {
		t.Fatalf("UpdatePrintHistorySpool() error = %v", err)
	}

	updatedHistory, err := bridge.GetPrintHistory(10)
	if err != nil {
		t.Fatalf("GetPrintHistory() after update error = %v", err)
	}
	if updatedHistory[0].SpoolID == nil || *updatedHistory[0].SpoolID != 20 {
		t.Fatalf("updated spool id = %v, want 20", updatedHistory[0].SpoolID)
	}

	if got := spoolman.usedWeight(10); got != 37.5 {
		t.Fatalf("old spool used weight = %.2f, want 37.50", got)
	}
	if got := spoolman.usedWeight(20); got != 17.5 {
		t.Fatalf("new spool used weight = %.2f, want 17.50", got)
	}
}

func TestUpdatePrintHistorySpoolAssignsUsageFromUnknownSpool(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()

	bridge := newTestBridge(t, spoolman.server.URL)

	if err := bridge.LogPrintUsage("Printer A", 0, nil, 12.5, "cube.gcode"); err != nil {
		t.Fatalf("LogPrintUsage() error = %v", err)
	}

	history, err := bridge.GetPrintHistory(10)
	if err != nil {
		t.Fatalf("GetPrintHistory() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history length = %d, want 1", len(history))
	}
	if history[0].SpoolID != nil {
		t.Fatalf("initial spool id = %v, want nil", history[0].SpoolID)
	}

	if err := bridge.UpdatePrintHistorySpool(history[0].ID, historyIntPointer(20)); err != nil {
		t.Fatalf("UpdatePrintHistorySpool() error = %v", err)
	}

	updatedHistory, err := bridge.GetPrintHistory(10)
	if err != nil {
		t.Fatalf("GetPrintHistory() after update error = %v", err)
	}
	if updatedHistory[0].SpoolID == nil || *updatedHistory[0].SpoolID != 20 {
		t.Fatalf("updated spool id = %v, want 20", updatedHistory[0].SpoolID)
	}

	if got := spoolman.usedWeight(10); got != 50 {
		t.Fatalf("old spool used weight = %.2f, want 50.00", got)
	}
	if got := spoolman.usedWeight(20); got != 17.5 {
		t.Fatalf("new spool used weight = %.2f, want 17.50", got)
	}
}

func TestUpdatePrintHistorySpoolCanClearAssignedSpool(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()

	bridge := newTestBridge(t, spoolman.server.URL)

	if err := bridge.LogPrintUsage("Printer A", 0, historyIntPointer(10), 12.5, "cube.gcode"); err != nil {
		t.Fatalf("LogPrintUsage() error = %v", err)
	}

	history, err := bridge.GetPrintHistory(10)
	if err != nil {
		t.Fatalf("GetPrintHistory() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history length = %d, want 1", len(history))
	}

	if err := bridge.UpdatePrintHistorySpool(history[0].ID, nil); err != nil {
		t.Fatalf("UpdatePrintHistorySpool() error = %v", err)
	}

	updatedHistory, err := bridge.GetPrintHistory(10)
	if err != nil {
		t.Fatalf("GetPrintHistory() after update error = %v", err)
	}
	if updatedHistory[0].SpoolID != nil {
		t.Fatalf("updated spool id = %v, want nil", updatedHistory[0].SpoolID)
	}

	if got := spoolman.usedWeight(10); got != 37.5 {
		t.Fatalf("old spool used weight = %.2f, want 37.50", got)
	}
	if got := spoolman.usedWeight(20); got != 5 {
		t.Fatalf("new spool used weight = %.2f, want 5.00", got)
	}
}

func TestHandlePrusaLinkPrintFinishedUsesJobFilamentMetadata(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()

	bridge := newTestBridge(t, spoolman.server.URL)

	if err := bridge.SetToolheadMapping("Printer A", 0, 20); err != nil {
		t.Fatalf("SetToolheadMapping() error = %v", err)
	}

	err := bridge.handlePrusaLinkPrintFinished(
		PrinterConfig{
			Name:      "Printer A",
			IPAddress: "127.0.0.1:1",
		},
		"usb/test.gcode",
		map[int]float64{0: 12.5},
	)
	if err != nil {
		t.Fatalf("handlePrusaLinkPrintFinished() error = %v", err)
	}

	if got := spoolman.usedWeight(20); got != 17.5 {
		t.Fatalf("spool used weight = %.2f, want 17.50", got)
	}

	history, err := bridge.GetPrintHistory(20)
	if err != nil {
		t.Fatalf("GetPrintHistory() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history length = %d, want 1", len(history))
	}
	if history[0].FilamentUsed != 12.5 {
		t.Fatalf("history filament used = %.2f, want 12.50", history[0].FilamentUsed)
	}
	if history[0].SpoolID == nil || *history[0].SpoolID != 20 {
		t.Fatalf("history spool id = %v, want 20", history[0].SpoolID)
	}
}

func TestHandlePrusaLinkPrintFinishedFallsBackToFileMetadata(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()

	prusaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/files/usb/test.gcode":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"meta":{"filament used [g] per tool":[7.25]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer prusaServer.Close()

	bridge := newTestBridge(t, spoolman.server.URL)

	if err := bridge.SetToolheadMapping("Printer A", 0, 20); err != nil {
		t.Fatalf("SetToolheadMapping() error = %v", err)
	}

	err := bridge.handlePrusaLinkPrintFinished(
		PrinterConfig{
			Name:      "Printer A",
			IPAddress: strings.TrimPrefix(prusaServer.URL, "http://"),
		},
		"usb/test.gcode",
		nil,
	)
	if err != nil {
		t.Fatalf("handlePrusaLinkPrintFinished() error = %v", err)
	}

	if got := spoolman.usedWeight(20); got != 12.25 {
		t.Fatalf("spool used weight = %.2f, want 12.25", got)
	}
}

func TestProcessFilamentUsageLogsUnknownSpoolWhenToolheadUnmapped(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()

	bridge := newTestBridge(t, spoolman.server.URL)

	if err := bridge.processFilamentUsage("Printer A", map[int]float64{0: 8.5}, "unknown.gcode"); err != nil {
		t.Fatalf("processFilamentUsage() error = %v", err)
	}

	history, err := bridge.GetPrintHistory(10)
	if err != nil {
		t.Fatalf("GetPrintHistory() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history length = %d, want 1", len(history))
	}
	if history[0].SpoolID != nil {
		t.Fatalf("history spool id = %v, want nil", history[0].SpoolID)
	}
	if history[0].FilamentUsed != 8.5 {
		t.Fatalf("history filament used = %.2f, want 8.50", history[0].FilamentUsed)
	}
	if got := spoolman.usedWeight(10); got != 50 {
		t.Fatalf("spool 10 used weight = %.2f, want 50.00", got)
	}
	if got := spoolman.usedWeight(20); got != 5 {
		t.Fatalf("spool 20 used weight = %.2f, want 5.00", got)
	}
}
