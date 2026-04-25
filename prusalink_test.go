package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type errorAfterChunksReader struct {
	chunks [][]byte
	index  int
	err    error
}

func (r *errorAfterChunksReader) Read(p []byte) (int, error) {
	if r.index >= len(r.chunks) {
		if r.err != nil {
			return 0, r.err
		}
		return 0, io.EOF
	}

	chunk := r.chunks[r.index]
	r.index++
	readCount := copy(p, chunk)
	if r.index >= len(r.chunks) && r.err != nil {
		return readCount, r.err
	}
	return readCount, nil
}

func TestParseGcodeFilamentUsageFromReaderReturnsUsageBeforeReadError(t *testing.T) {
	client := NewPrusaLinkClient("127.0.0.1:1", "", 5, 5)

	usage, err := client.ParseGcodeFilamentUsageFromReader(&errorAfterChunksReader{
		chunks: [][]byte{
			[]byte("header\x00filament used [g]=29.19"),
		},
		err: errors.New("spotty wifi"),
	})
	if err != nil {
		t.Fatalf("ParseGcodeFilamentUsageFromReader() error = %v", err)
	}
	if len(usage) != 1 {
		t.Fatalf("usage size = %d, want 1", len(usage))
	}
	if got := usage[0]; got != 29.19 {
		t.Fatalf("usage[0] = %.2f, want 29.19", got)
	}
}

func TestGetFilamentUsageForFileUsesDownloadRefRawEndpoint(t *testing.T) {
	var sawRangeHeader string

	prusaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/files/usb/test.bgcode":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"meta":{},"refs":{"download":"/api/files/usb/test.bgcode/raw"}}`)
		case "/api/files/usb/test.bgcode/raw":
			sawRangeHeader = r.Header.Get("Range")
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("junk\x00filament used [g]=29.19\x00tail"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer prusaServer.Close()

	client := NewPrusaLinkClient(strings.TrimPrefix(prusaServer.URL, "http://"), "", 5, 5)

	usage, err := client.GetFilamentUsageForFile("usb/test.bgcode")
	if err != nil {
		t.Fatalf("GetFilamentUsageForFile() error = %v", err)
	}
	if got := usage[0]; got != 29.19 {
		t.Fatalf("usage[0] = %.2f, want 29.19", got)
	}
	if sawRangeHeader != fmt.Sprintf("bytes=0-%d", prusaLinkDownloadSniffBytes-1) {
		t.Fatalf("Range header = %q, want %q", sawRangeHeader, fmt.Sprintf("bytes=0-%d", prusaLinkDownloadSniffBytes-1))
	}
}
