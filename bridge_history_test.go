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

func TestUpdatePrintHistoryAdjustsSpoolUsageWhenWeightChanges(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()

	bridge := newTestBridge(t, spoolman.server.URL)

	if err := bridge.spoolman.AdjustSpoolUsage(20, 12.5); err != nil {
		t.Fatalf("AdjustSpoolUsage() error = %v", err)
	}

	if err := bridge.LogPrintUsage("Printer A", 0, historyIntPointer(20), 12.5, "cube.gcode"); err != nil {
		t.Fatalf("LogPrintUsage() error = %v", err)
	}

	history, err := bridge.GetPrintHistory(10)
	if err != nil {
		t.Fatalf("GetPrintHistory() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history length = %d, want 1", len(history))
	}

	if err := bridge.UpdatePrintHistory(history[0].ID, historyIntPointer(20), 15.75); err != nil {
		t.Fatalf("UpdatePrintHistory() error = %v", err)
	}

	updatedHistory, err := bridge.GetPrintHistory(10)
	if err != nil {
		t.Fatalf("GetPrintHistory() after update error = %v", err)
	}
	if updatedHistory[0].FilamentUsed != 15.75 {
		t.Fatalf("updated filament used = %.2f, want 15.75", updatedHistory[0].FilamentUsed)
	}

	if got := spoolman.usedWeight(20); got != 20.75 {
		t.Fatalf("spool used weight = %.2f, want 20.75", got)
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
		"test.gcode",
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
		"test.gcode",
		nil,
	)
	if err != nil {
		t.Fatalf("handlePrusaLinkPrintFinished() error = %v", err)
	}

	if got := spoolman.usedWeight(20); got != 12.25 {
		t.Fatalf("spool used weight = %.2f, want 12.25", got)
	}
}

func TestHandlePrusaLinkPrintFinishedUsesSnakeCaseFileMetadata(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()

	prusaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/files/usb/test.gcode":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"meta":{"filament_used_g_per_tool":[8.5]}}`)
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
		"test.gcode",
		nil,
	)
	if err != nil {
		t.Fatalf("handlePrusaLinkPrintFinished() error = %v", err)
	}

	if got := spoolman.usedWeight(20); got != 13.5 {
		t.Fatalf("spool used weight = %.2f, want 13.50", got)
	}
}

func TestHandlePrusaLinkPrintFinishedFallsBackToDownloadedFileParse(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()

	prusaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/files/usb/test.bgcode":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"meta":{}}`)
		case "/usb/test.bgcode":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprint(w, "header\nfilament used [g]=9.75\nfooter\n")
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
		"usb/test.bgcode",
		"test.bgcode",
		nil,
	)
	if err != nil {
		t.Fatalf("handlePrusaLinkPrintFinished() error = %v", err)
	}

	if got := spoolman.usedWeight(20); got != 14.75 {
		t.Fatalf("spool used weight = %.2f, want 14.75", got)
	}
}

func TestHandlePrusaLinkPrintFinishedLogsUnknownWeightWhenMetadataMissing(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()

	prusaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/files/usb/test.gcode":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"meta":{}}`)
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
			Toolheads: 1,
		},
		"usb/test.gcode",
		"test.gcode",
		nil,
	)
	if err != nil {
		t.Fatalf("handlePrusaLinkPrintFinished() error = %v", err)
	}

	history, err := bridge.GetPrintHistory(10)
	if err != nil {
		t.Fatalf("GetPrintHistory() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history length = %d, want 1", len(history))
	}
	if history[0].FilamentUsed != 0 {
		t.Fatalf("history filament used = %.2f, want 0", history[0].FilamentUsed)
	}
	if history[0].SpoolID == nil || *history[0].SpoolID != 20 {
		t.Fatalf("history spool id = %v, want 20", history[0].SpoolID)
	}

	printErrors := bridge.GetPrintErrors()
	if len(printErrors) != 1 {
		t.Fatalf("print errors = %d, want 1", len(printErrors))
	}

	if got := spoolman.usedWeight(20); got != 5 {
		t.Fatalf("spool used weight = %.2f, want 5.00", got)
	}
}

func TestProcessFilamentUsageLogsUnknownSpoolWhenToolheadUnmapped(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()

	bridge := newTestBridge(t, spoolman.server.URL)

	if err := bridge.processFilamentUsage("Printer A", map[int]float64{0: 8.5}, "unknown.gcode", "usb/unknown.gcode"); err != nil {
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

func TestMonitorPrusaLinkUsesDisplayNameForFinishedHistory(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()

	var (
		mu          sync.Mutex
		statusCalls int
	)

	prusaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/status":
			mu.Lock()
			statusCalls++
			call := statusCalls
			mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			if call == 1 {
				_, _ = fmt.Fprint(w, `{"job":{"id":1,"progress":25,"time_remaining":1200,"time_printing":300},"printer":{"state":"PRINTING"}}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"job":{"id":1,"progress":100,"time_remaining":0,"time_printing":1800},"printer":{"state":"IDLE"}}`)
		case "/api/v1/job":
			mu.Lock()
			call := statusCalls
			mu.Unlock()

			if call == 1 {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"id":1,"state":"PRINTING","progress":25,"time_remaining":1200,"time_printing":300,"file":{"name":"MERGED~1.BGC","display_name":"merged-widget.bgcode","path":"usb","meta":{}}}`)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/files/usb/MERGED~1.BGC":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"meta":{"filament_used_g_per_tool":[11.5]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer prusaServer.Close()

	bridge := newTestBridge(t, spoolman.server.URL)

	if err := bridge.SetToolheadMapping("Printer A", 0, 20); err != nil {
		t.Fatalf("SetToolheadMapping() error = %v", err)
	}

	config := PrinterConfig{
		Name:      "Printer A",
		IPAddress: strings.TrimPrefix(prusaServer.URL, "http://"),
		Toolheads: 1,
	}

	if err := bridge.monitorPrusaLink("printer-a", config); err != nil {
		t.Fatalf("first monitorPrusaLink() error = %v", err)
	}
	if err := bridge.monitorPrusaLink("printer-a", config); err != nil {
		t.Fatalf("second monitorPrusaLink() error = %v", err)
	}

	history, err := bridge.GetPrintHistory(10)
	if err != nil {
		t.Fatalf("GetPrintHistory() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history length = %d, want 1", len(history))
	}
	if history[0].JobName != "merged-widget.bgcode" {
		t.Fatalf("history job name = %q, want %q", history[0].JobName, "merged-widget.bgcode")
	}
}

func TestRefreshPrintHistoryFilamentUsageUsesStoredSourcePath(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()

	prusaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/files/usb/test.bgcode":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"meta":{},"refs":{"download":"/api/files/usb/test.bgcode/raw"}}`)
		case "/api/files/usb/test.bgcode/raw":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("junk\nfilament used [g]=29.19\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer prusaServer.Close()

	bridge := newTestBridge(t, spoolman.server.URL)

	if err := bridge.SavePrinterConfig("printer-a", PrinterConfig{
		Name:      "Printer A",
		Model:     ModelCoreOne,
		IPAddress: strings.TrimPrefix(prusaServer.URL, "http://"),
		Toolheads: 1,
	}); err != nil {
		t.Fatalf("SavePrinterConfig() error = %v", err)
	}

	if err := bridge.spoolman.AdjustSpoolUsage(20, 12.5); err != nil {
		t.Fatalf("AdjustSpoolUsage() error = %v", err)
	}
	if err := bridge.LogPrintUsageWithSourcePath("Printer A", 0, historyIntPointer(20), 12.5, "test.bgcode", "usb/test.bgcode"); err != nil {
		t.Fatalf("LogPrintUsageWithSourcePath() error = %v", err)
	}

	history, err := bridge.GetPrintHistory(10)
	if err != nil {
		t.Fatalf("GetPrintHistory() error = %v", err)
	}

	updatedEntry, err := bridge.RefreshPrintHistoryFilamentUsage(history[0].ID, historyIntPointer(20))
	if err != nil {
		t.Fatalf("RefreshPrintHistoryFilamentUsage() error = %v", err)
	}
	if updatedEntry.FilamentUsed != 29.19 {
		t.Fatalf("updated filament used = %.2f, want 29.19", updatedEntry.FilamentUsed)
	}
	if updatedEntry.SourcePath != "usb/test.bgcode" {
		t.Fatalf("updated source path = %q, want %q", updatedEntry.SourcePath, "usb/test.bgcode")
	}
	if got := spoolman.usedWeight(20); got != 34.19 {
		t.Fatalf("spool used weight = %.2f, want 34.19", got)
	}
}

func TestRefreshPrintHistoryFilamentUsageFindsPathFromJobName(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()

	prusaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/storage":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"storage_list":[{"path":"/usb","available":true,"type":"USB"}]}`)
		case "/api/v1/files/usb/cube.bgcode":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"meta":{"filament_used_g_per_tool":[18.75]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer prusaServer.Close()

	bridge := newTestBridge(t, spoolman.server.URL)

	if err := bridge.SavePrinterConfig("printer-a", PrinterConfig{
		Name:      "Printer A",
		Model:     ModelCoreOne,
		IPAddress: strings.TrimPrefix(prusaServer.URL, "http://"),
		Toolheads: 1,
	}); err != nil {
		t.Fatalf("SavePrinterConfig() error = %v", err)
	}

	if err := bridge.LogPrintUsage("Printer A", 0, nil, 0, "cube.bgcode"); err != nil {
		t.Fatalf("LogPrintUsage() error = %v", err)
	}

	history, err := bridge.GetPrintHistory(10)
	if err != nil {
		t.Fatalf("GetPrintHistory() error = %v", err)
	}

	updatedEntry, err := bridge.RefreshPrintHistoryFilamentUsage(history[0].ID, nil)
	if err != nil {
		t.Fatalf("RefreshPrintHistoryFilamentUsage() error = %v", err)
	}
	if updatedEntry.FilamentUsed != 18.75 {
		t.Fatalf("updated filament used = %.2f, want 18.75", updatedEntry.FilamentUsed)
	}
	if updatedEntry.SourcePath != "usb/cube.bgcode" {
		t.Fatalf("updated source path = %q, want %q", updatedEntry.SourcePath, "usb/cube.bgcode")
	}
}
