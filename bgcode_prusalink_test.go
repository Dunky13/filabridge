package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

func TestPrusaLinkDownloadDecodesBGCodeMetadata(t *testing.T) {
	fixture, err := os.ReadFile("testdata/bgcode/official-alpha11-source.bgcode")
	if err != nil {
		t.Fatalf("read BGCode fixture: %v", err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(fixture)
	}))
	defer server.Close()

	client, err := NewPrusaLinkClientWithOptions(PrusaLinkClientOptions{
		BaseURL:             server.URL,
		Timeout:             5,
		FileDownloadTimeout: 5,
	})
	if err != nil {
		t.Fatalf("NewPrusaLinkClientWithOptions() error = %v", err)
	}
	usage, err := client.GetFilamentUsageFromDownloadWithRetry("usb/eight-tool.bgcode", "/download", 5)
	if err != nil {
		t.Fatalf("GetFilamentUsageFromDownloadWithRetry() error = %v", err)
	}
	if len(usage) != 2 || usage[0] != 1.25 || usage[2] != 2.75 {
		t.Fatalf("usage = %#v, want map[0:1.25 2:2.75]", usage)
	}
	if requests.Load() != 1 {
		t.Fatalf("download requests = %d, want one full BGCode request", requests.Load())
	}
}

func TestParseGcodeFilamentUsageAutoDetectsBGCode(t *testing.T) {
	fixture, err := os.ReadFile("testdata/bgcode/official-alpha11-source.bgcode")
	if err != nil {
		t.Fatalf("read BGCode fixture: %v", err)
	}
	client := NewPrusaLinkClient("printer.local", "", 5, 5)
	usage, err := client.ParseGcodeFilamentUsage(fixture)
	if err != nil {
		t.Fatalf("ParseGcodeFilamentUsage() error = %v", err)
	}
	if usage[0] != 1.25 || usage[2] != 2.75 {
		t.Fatalf("usage = %#v, want BGCode metadata values", usage)
	}
}
