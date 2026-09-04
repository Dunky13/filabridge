package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPrinterAPIKeepsConnectionSecretsWriteOnly(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()
	bridge := newTestBridge(t, spoolman.server.URL)
	original := PrinterConfig{
		Name:                 "Printer A",
		Model:                "MK4S",
		IPAddress:            "printer.local",
		APIKey:               "api-secret",
		PrusaLinkUsername:    "operator",
		PrusaLinkPassword:    "digest-secret",
		PrusaLinkCustomCAPEM: "certificate-secret",
		Toolheads:            1,
	}
	if err := bridge.SavePrinterConfig("printer-a", original); err != nil {
		t.Fatalf("SavePrinterConfig() error = %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	server := &WebServer{bridge: bridge}
	router.GET("/api/printers", server.getPrintersHandler)
	router.PUT("/api/printers/:id", server.updatePrinterHandler)

	getRecorder := httptest.NewRecorder()
	router.ServeHTTP(getRecorder, httptest.NewRequest(http.MethodGet, "/api/printers", nil))
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d: %s", getRecorder.Code, getRecorder.Body.String())
	}
	var response struct {
		Printers map[string]map[string]interface{} `json:"printers"`
	}
	if err := json.Unmarshal(getRecorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	printer := response.Printers["printer-a"]
	for _, secretKey := range []string{"api_key", "prusalink_password", "prusalink_custom_ca_pem"} {
		if _, exposed := printer[secretKey]; exposed {
			t.Fatalf("GET /api/printers exposed %s", secretKey)
		}
	}
	if printer["api_key_configured"] != true || printer["prusalink_password_configured"] != true || printer["prusalink_custom_ca_configured"] != true {
		t.Fatalf("configured flags = %#v, want all true", printer)
	}

	updateRecorder := httptest.NewRecorder()
	updateRequest := httptest.NewRequest(http.MethodPut, "/api/printers/printer-a", strings.NewReader(`{
		"name":"Renamed Printer","model":"MK4S","ip_address":"printer.local",
		"api_key":"","prusalink_username":"operator","prusalink_password":"",
		"prusalink_custom_ca_pem":"","toolheads":1
	}`))
	updateRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(updateRecorder, updateRequest)
	if updateRecorder.Code != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", updateRecorder.Code, updateRecorder.Body.String())
	}
	configs, err := bridge.GetAllPrinterConfigs()
	if err != nil {
		t.Fatalf("GetAllPrinterConfigs() error = %v", err)
	}
	updated := configs["printer-a"]
	if updated.APIKey != original.APIKey || updated.PrusaLinkPassword != original.PrusaLinkPassword || updated.PrusaLinkCustomCAPEM != original.PrusaLinkCustomCAPEM {
		t.Fatalf("blank update erased stored secrets: %#v", updated)
	}
	exfilRecorder := httptest.NewRecorder()
	exfilRequest := httptest.NewRequest(http.MethodPut, "/api/printers/printer-a", strings.NewReader(`{
		"name":"Renamed Printer","model":"MK4S","ip_address":"attacker.invalid","toolheads":1
	}`))
	exfilRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(exfilRecorder, exfilRequest)
	if exfilRecorder.Code != http.StatusBadRequest {
		t.Fatalf("origin-changing PUT status = %d, want 400", exfilRecorder.Code)
	}

	clearRecorder := httptest.NewRecorder()
	clearRequest := httptest.NewRequest(http.MethodPut, "/api/printers/printer-a", strings.NewReader(`{
		"name":"Renamed Printer","model":"MK4S","ip_address":"printer.local","toolheads":1,
		"clear_api_key":true,"clear_prusalink_credentials":true,"clear_prusalink_custom_ca_pem":true
	}`))
	clearRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(clearRecorder, clearRequest)
	if clearRecorder.Code != http.StatusOK {
		t.Fatalf("clear PUT status = %d: %s", clearRecorder.Code, clearRecorder.Body.String())
	}
	configs, err = bridge.GetAllPrinterConfigs()
	if err != nil {
		t.Fatalf("GetAllPrinterConfigs(after clear) error = %v", err)
	}
	cleared := configs["printer-a"]
	if cleared.APIKey != "" || cleared.PrusaLinkUsername != "" || cleared.PrusaLinkPassword != "" || cleared.PrusaLinkCustomCAPEM != "" {
		t.Fatalf("explicit clear retained connection secrets: %#v", cleared)
	}
}
