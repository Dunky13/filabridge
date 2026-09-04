package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func newNFCSecurityServer(t *testing.T) (*historyTestSpoolmanServer, *FilamentBridge, *WebServer) {
	t.Helper()
	t.Setenv("FILABRIDGE_WEB_USERNAME", "operator")
	t.Setenv("FILABRIDGE_WEB_PASSWORD", "correct horse")
	t.Setenv("FILABRIDGE_PUBLIC_ORIGIN", "")
	spoolman := newHistoryTestSpoolmanServer()
	t.Cleanup(spoolman.close)
	bridge := newTestBridge(t, spoolman.server.URL)
	web := NewWebServerForHost(bridge, "0.0.0.0")
	t.Cleanup(web.Shutdown)
	return spoolman, bridge, web
}

func nfcFormRequest(server *WebServer, method, target string, values url.Values, origin string) *httptest.ResponseRecorder {
	body := ""
	if values != nil {
		body = values.Encode()
	}
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if values != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	request.SetBasicAuth("operator", "correct horse")
	recorder := httptest.NewRecorder()
	server.router.ServeHTTP(recorder, request)
	return recorder
}

func TestNFCAssignmentGETOnlyRendersConfirmation(t *testing.T) {
	spoolman, bridge, server := newNFCSecurityServer(t)

	response := nfcFormRequest(server, http.MethodGet, "/api/nfc/assign?spool=20", nil, "http://evil.invalid")
	if response.Code != http.StatusOK {
		t.Fatalf("cross-site GET status = %d, want confirmation 200: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "Confirm NFC scan") {
		t.Fatalf("GET response did not render confirmation: %s", response.Body.String())
	}
	assertNoNFCSideEffects(t, spoolman, bridge)

	escaped := nfcFormRequest(server, http.MethodGet, "/api/nfc/assign?location=%3Cscript%3Ealert%281%29%3C%2Fscript%3E", nil, "")
	if strings.Contains(escaped.Body.String(), "<script>alert(1)</script>") || !strings.Contains(escaped.Body.String(), "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("confirmation did not HTML-escape location: %s", escaped.Body.String())
	}
}

func TestNFCAssignmentPOSTRejectsCrossOriginBeforeMutation(t *testing.T) {
	spoolman, bridge, server := newNFCSecurityServer(t)

	response := nfcFormRequest(server, http.MethodPost, "/api/nfc/assign", url.Values{"spool": {"20"}}, "http://evil.invalid")
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin POST status = %d, want 403: %s", response.Code, response.Body.String())
	}
	assertNoNFCSideEffects(t, spoolman, bridge)
}

func TestNFCAssignmentSameOriginConfirmCompletesAssignment(t *testing.T) {
	spoolman, bridge, server := newNFCSecurityServer(t)
	origin := "http://example.com"

	spool := nfcFormRequest(server, http.MethodPost, "/api/nfc/assign", url.Values{"spool": {"20"}}, origin)
	if spool.Code != http.StatusOK || !strings.Contains(spool.Body.String(), "Now scan a location tag") {
		t.Fatalf("spool confirmation status/body = %d/%s", spool.Code, spool.Body.String())
	}
	location := nfcFormRequest(server, http.MethodPost, "/api/nfc/assign", url.Values{"location": {"Printer A - Toolhead 0"}}, origin)
	if location.Code != http.StatusOK || !strings.Contains(location.Body.String(), "Assignment Complete") {
		t.Fatalf("location confirmation status/body = %d/%s", location.Code, location.Body.String())
	}

	mapping, err := bridge.GetToolheadMapping("printer-a", 0)
	if err != nil || mapping != 20 {
		t.Fatalf("toolhead mapping = %d, err=%v; want spool 20", mapping, err)
	}
	var sessions int
	if err := bridge.db.QueryRow("SELECT COUNT(*) FROM nfc_sessions").Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("completed NFC sessions = %d, want 0", sessions)
	}
	spoolman.mu.Lock()
	patches := spoolman.patchCounts[20]
	spoolman.mu.Unlock()
	if patches == 0 {
		t.Fatal("same-origin confirmation did not update Spoolman")
	}
}

func assertNoNFCSideEffects(t *testing.T, spoolman *historyTestSpoolmanServer, bridge *FilamentBridge) {
	t.Helper()
	var sessions int
	if err := bridge.db.QueryRow("SELECT COUNT(*) FROM nfc_sessions").Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("NFC session count = %d, want 0", sessions)
	}
	mappings, err := bridge.GetToolheadMappings("printer-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 0 {
		t.Fatalf("toolhead mappings changed: %+v", mappings)
	}
	spoolman.mu.Lock()
	patches := spoolman.patchCounts[20]
	spoolman.mu.Unlock()
	if patches != 0 {
		t.Fatalf("Spoolman patch calls = %d, want 0", patches)
	}
}
