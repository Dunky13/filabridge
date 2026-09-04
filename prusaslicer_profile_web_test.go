package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"filabridge/profilesync"
)

func TestAuthenticatedProfileExportUsesFilamentDefinitionsOnly(t *testing.T) {
	t.Setenv("FILABRIDGE_WEB_USERNAME", "operator")
	t.Setenv("FILABRIDGE_WEB_PASSWORD", "secret")
	var requestedPaths []string
	spoolman := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPaths = append(requestedPaths, r.URL.Path)
		if r.URL.Path != "/api/v1/filament" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[
			{"id":2,"name":"Blue PETG","material":"PETG","price":15,"weight":500,"density":1.27,"diameter":1.75,"spool_weight":190,"color_hex":"0066cc","vendor":{"id":3,"name":"Acme"}},
			{"id":1,"name":"Old PLA","material":"PLA","archived":true}
		]`)
	}))
	defer spoolman.Close()

	bridge := newTestBridge(t, spoolman.URL)
	server := NewWebServerForHost(bridge, "0.0.0.0")
	t.Cleanup(server.Shutdown)

	unauthorized := requestRouter(t, server, http.MethodGet, "/api/prusaslicer/profiles.zip", "", "", "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", unauthorized.Code)
	}
	response := requestRouter(t, server, http.MethodGet, "/api/prusaslicer/profiles.zip", "", "operator", "secret")
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := response.Header().Get("Content-Disposition"); !strings.Contains(got, "filabridge-prusaslicer-profiles-") {
		t.Errorf("Content-Disposition = %q", got)
	}
	if got := response.Header().Get("ETag"); got == "" {
		t.Error("ETag is empty")
	}
	if len(requestedPaths) != 1 || requestedPaths[0] != "/api/v1/filament" {
		t.Fatalf("Spoolman requests = %v, want filament definitions only", requestedPaths)
	}

	reader, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		t.Fatalf("open response zip: %v", err)
	}
	var manifest profilesync.Manifest
	var profile []byte
	for _, file := range reader.File {
		if file.Name == profilesync.ManagedProfilePath {
			input, openErr := file.Open()
			if openErr != nil {
				t.Fatal(openErr)
			}
			profile, err = io.ReadAll(input)
			_ = input.Close()
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		if file.Name != "manifest.json" {
			continue
		}
		input, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		err = json.NewDecoder(input).Decode(&manifest)
		_ = input.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(manifest.FilamentIDs) != 1 || manifest.FilamentIDs[0] != 2 {
		t.Fatalf("exported filament IDs = %v, want active definition 2 only", manifest.FilamentIDs)
	}
	if !strings.Contains(string(profile), `filament_cost: "30"`) {
		t.Fatalf("profile did not convert 15 currency/500g to 30 currency/kg:\n%s", profile)
	}
}

func TestProfileExportFailsClosedWithoutCredentialsEvenOnLoopback(t *testing.T) {
	t.Setenv("FILABRIDGE_WEB_USERNAME", "")
	t.Setenv("FILABRIDGE_WEB_PASSWORD", "")
	spoolmanRequests := 0
	spoolman := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		spoolmanRequests++
		_, _ = io.WriteString(w, `[]`)
	}))
	defer spoolman.Close()
	bridge := newTestBridge(t, spoolman.URL)
	server := NewWebServerForHost(bridge, "127.0.0.1")
	t.Cleanup(server.Shutdown)

	response := requestRouter(t, server, http.MethodGet, "/api/prusaslicer/profiles.zip", "", "", "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("profile export status = %d, want 503 without credentials", response.Code)
	}
	if spoolmanRequests != 0 {
		t.Fatalf("profile export made %d Spoolman requests before authentication", spoolmanRequests)
	}
}
