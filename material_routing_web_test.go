package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestLogicalToolRouteHandlersConfigureINDXRouting(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()
	bridge := newTestBridge(t, spoolman.server.URL)
	if err := bridge.SavePrinterConfig("core-one-indx", PrinterConfig{Name: "CORE One INDX", Model: "CORE One INDX", IPAddress: "printer.local", Toolheads: 8}); err != nil {
		t.Fatalf("SavePrinterConfig() error = %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	server := &WebServer{bridge: bridge}
	router.PUT("/api/printers/:id/tool-routes/:logical_tool_id", server.updateLogicalToolRouteHandler)
	router.GET("/api/printers/:id/tool-routes", server.getLogicalToolRoutesHandler)

	putRecorder := httptest.NewRecorder()
	putRequest := httptest.NewRequest(http.MethodPut, "/api/printers/core-one-indx/tool-routes/0", strings.NewReader(`{"physical_toolhead_id":7}`))
	putRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(putRecorder, putRequest)
	if putRecorder.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200: %s", putRecorder.Code, putRecorder.Body.String())
	}

	getRecorder := httptest.NewRecorder()
	getRequest := httptest.NewRequest(http.MethodGet, "/api/printers/core-one-indx/tool-routes", nil)
	router.ServeHTTP(getRecorder, getRequest)
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200: %s", getRecorder.Code, getRecorder.Body.String())
	}
	var response struct {
		Routes map[int]int `json:"routes"`
	}
	if err := json.Unmarshal(getRecorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Routes[0] != 7 {
		t.Fatalf("logical tool 0 route = %d, want physical toolhead 7", response.Routes[0])
	}
}
