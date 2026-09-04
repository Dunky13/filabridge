package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPrinterPresetsHandlerReturnsPresetCatalog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	server := &WebServer{}
	router.GET("/api/printer-presets", server.printerPresetsHandler)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/printer-presets", nil)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var response struct {
		Presets []PrinterPreset `json:"presets"`
		Custom  string          `json:"custom_preset_id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Presets) < 10 {
		t.Errorf("got %d presets, want current Prusa catalog", len(response.Presets))
	}
	if response.Custom != CustomPrinterPresetID {
		t.Errorf("custom preset id = %q, want %q", response.Custom, CustomPrinterPresetID)
	}
}
