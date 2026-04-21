package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type testSpoolmanServer struct {
	server         *httptest.Server
	mu             sync.Mutex
	locations      []SpoolmanLocation
	spoolLocations map[int]string
}

func newTestSpoolmanServer() *testSpoolmanServer {
	ts := &testSpoolmanServer{
		locations: []SpoolmanLocation{
			{ID: 1, Name: "Shelf"},
			{ID: 2, Name: "Drybox"},
		},
		spoolLocations: make(map[int]string),
	}

	ts.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/location":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ts.locations)
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/v1/spool/"):
			spoolID, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/spool/"))
			if err != nil {
				http.Error(w, "bad spool id", http.StatusBadRequest)
				return
			}

			var payload struct {
				Location string `json:"location"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}

			ts.mu.Lock()
			ts.spoolLocations[spoolID] = payload.Location
			ts.mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))

	return ts
}

func (ts *testSpoolmanServer) close() {
	ts.server.Close()
}

func (ts *testSpoolmanServer) spoolLocation(spoolID int) string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.spoolLocations[spoolID]
}

func newTestBridge(t *testing.T, spoolmanURL string) *FilamentBridge {
	t.Helper()

	config := &Config{
		DBFile:          filepath.Join(t.TempDir(), "filabridge.db"),
		SpoolmanURL:     spoolmanURL,
		SpoolmanTimeout: 5,
	}

	bridge, err := NewFilamentBridge(config)
	if err != nil {
		t.Fatalf("NewFilamentBridge() error = %v", err)
	}

	t.Cleanup(func() {
		_ = bridge.db.Close()
	})

	return bridge
}

func TestSwitchToolheadSpoolUsesExplicitPreviousLocation(t *testing.T) {
	spoolman := newTestSpoolmanServer()
	defer spoolman.close()

	bridge := newTestBridge(t, spoolman.server.URL)

	if err := bridge.SetToolheadMapping("Printer A", 0, 10); err != nil {
		t.Fatalf("SetToolheadMapping() error = %v", err)
	}

	if err := bridge.SwitchToolheadSpool("Printer A", 0, 20, "Shelf"); err != nil {
		t.Fatalf("SwitchToolheadSpool() error = %v", err)
	}

	mappings, err := bridge.GetToolheadMappings("Printer A")
	if err != nil {
		t.Fatalf("GetToolheadMappings() error = %v", err)
	}
	if mappings[0].SpoolID != 20 {
		t.Fatalf("toolhead 0 spool = %d, want 20", mappings[0].SpoolID)
	}
	if got := spoolman.spoolLocation(20); got != "Printer A - Toolhead 0" {
		t.Fatalf("new spool location = %q, want %q", got, "Printer A - Toolhead 0")
	}
	if got := spoolman.spoolLocation(10); got != "Shelf" {
		t.Fatalf("previous spool location = %q, want %q", got, "Shelf")
	}
}

func TestSwitchToolheadSpoolFallsBackToDefaultLocation(t *testing.T) {
	spoolman := newTestSpoolmanServer()
	defer spoolman.close()

	bridge := newTestBridge(t, spoolman.server.URL)

	if err := bridge.SetToolheadMapping("Printer A", 0, 11); err != nil {
		t.Fatalf("SetToolheadMapping() error = %v", err)
	}
	if err := bridge.SetAutoAssignPreviousSpoolEnabled(true); err != nil {
		t.Fatalf("SetAutoAssignPreviousSpoolEnabled() error = %v", err)
	}
	if err := bridge.SetAutoAssignPreviousSpoolLocation("Drybox"); err != nil {
		t.Fatalf("SetAutoAssignPreviousSpoolLocation() error = %v", err)
	}

	if err := bridge.SwitchToolheadSpool("Printer A", 0, 0, ""); err != nil {
		t.Fatalf("SwitchToolheadSpool() error = %v", err)
	}

	mappings, err := bridge.GetToolheadMappings("Printer A")
	if err != nil {
		t.Fatalf("GetToolheadMappings() error = %v", err)
	}
	if _, exists := mappings[0]; exists {
		t.Fatalf("toolhead 0 should be unmapped")
	}
	if got := spoolman.spoolLocation(11); got != "Drybox" {
		t.Fatalf("previous spool location = %q, want %q", got, "Drybox")
	}
}

func TestSwitchToolheadSpoolRequiresLocationWhenReplacingMappedSpool(t *testing.T) {
	spoolman := newTestSpoolmanServer()
	defer spoolman.close()

	bridge := newTestBridge(t, spoolman.server.URL)

	if err := bridge.SetToolheadMapping("Printer A", 0, 12); err != nil {
		t.Fatalf("SetToolheadMapping() error = %v", err)
	}

	err := bridge.SwitchToolheadSpool("Printer A", 0, 22, "")
	if err == nil {
		t.Fatalf("SwitchToolheadSpool() error = nil, want location error")
	}
	if !strings.Contains(err.Error(), "needs a storage location") {
		t.Fatalf("SwitchToolheadSpool() error = %q, want storage location error", err.Error())
	}

	mappings, getErr := bridge.GetToolheadMappings("Printer A")
	if getErr != nil {
		t.Fatalf("GetToolheadMappings() error = %v", getErr)
	}
	if mappings[0].SpoolID != 12 {
		t.Fatalf("toolhead 0 spool = %d, want 12", mappings[0].SpoolID)
	}
	if got := spoolman.spoolLocation(12); got != "" {
		t.Fatalf("previous spool location = %q, want empty", got)
	}
}
