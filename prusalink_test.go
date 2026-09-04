package main

import (
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetVersionUsesConfiguredBaseURLAndAPIKey(t *testing.T) {
	var gotAPIKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			http.NotFound(w, r)
			return
		}
		gotAPIKey = r.Header.Get("X-Api-Key")
		_, _ = io.WriteString(w, `{"api":"2.0.0","server":"2.1.3","text":"PrusaLink","hostname":"core-one","firmware":"6.10.1","printer":"1.3.0","capabilities":{"upload-by-put":true,"job-meta":true,"future-shape":{"enabled":true}}}`)
	}))
	defer server.Close()

	client, err := NewPrusaLinkClientWithOptions(PrusaLinkClientOptions{
		BaseURL:             server.URL + "/",
		APIKey:              "secret",
		Timeout:             5,
		FileDownloadTimeout: 5,
	})
	if err != nil {
		t.Fatalf("NewPrusaLinkClientWithOptions() error = %v", err)
	}

	version, err := client.GetVersion()
	if err != nil {
		t.Fatalf("GetVersion() error = %v", err)
	}
	if gotAPIKey != "secret" {
		t.Fatalf("X-Api-Key = %q, want secret", gotAPIKey)
	}
	if version.API != "2.0.0" || version.Firmware != "6.10.1" || !version.Capabilities.UploadByPut {
		t.Fatalf("GetVersion() = %+v", version)
	}
	if !version.Capabilities.Supports("job-meta") || version.Capabilities.Supports("future-shape") {
		t.Fatalf("capabilities = %+v, want future boolean capability preserved", version.Capabilities)
	}
}

func TestGetVersionTrustsConfiguredCertificateAuthority(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"api":"2.0.0","text":"PrusaLink"}`)
	}))
	defer server.Close()

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	client, err := NewPrusaLinkClientWithOptions(PrusaLinkClientOptions{
		BaseURL:     server.URL,
		CustomCAPEM: caPEM,
		Timeout:     5,
	})
	if err != nil {
		t.Fatalf("NewPrusaLinkClientWithOptions() error = %v", err)
	}

	if _, err := client.GetVersion(); err != nil {
		t.Fatalf("GetVersion() error = %v", err)
	}
}

func TestNewPrusaLinkClientRejectsInvalidCertificateAuthority(t *testing.T) {
	_, err := NewPrusaLinkClientWithOptions(PrusaLinkClientOptions{
		BaseURL:     "https://printer.local",
		CustomCAPEM: []byte("not a certificate"),
	})
	if err == nil || !strings.Contains(err.Error(), "certificate authority") {
		t.Fatalf("error = %v, want invalid certificate authority", err)
	}
}

func TestNewPrusaLinkClientRejectsCredentialBearingBaseURL(t *testing.T) {
	_, err := NewPrusaLinkClientWithOptions(PrusaLinkClientOptions{
		BaseURL: "https://admin:secret@printer.local",
	})
	if err == nil || !strings.Contains(err.Error(), "must not include credentials") {
		t.Fatalf("error = %v, want credential-bearing URL rejection", err)
	}
}

func TestGetVersionSupportsDigestAuthentication(t *testing.T) {
	requestCount := 0
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		authorization = r.Header.Get("Authorization")
		if !strings.HasPrefix(authorization, "Digest ") {
			w.Header().Set("WWW-Authenticate", `Digest realm="PrusaLink", nonce="test-nonce", algorithm=MD5, qop="auth"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"api":"2.0.0","text":"PrusaLink"}`)
	}))
	defer server.Close()

	client, err := NewPrusaLinkClientWithOptions(PrusaLinkClientOptions{
		BaseURL:        server.URL,
		DigestUsername: "maker",
		DigestPassword: "secret",
		Timeout:        5,
	})
	if err != nil {
		t.Fatalf("NewPrusaLinkClientWithOptions() error = %v", err)
	}

	if _, err := client.GetVersion(); err != nil {
		t.Fatalf("GetVersion() error = %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("request count = %d, want challenge plus authenticated retry", requestCount)
	}
	if !strings.Contains(authorization, `username="maker"`) {
		t.Fatalf("Authorization = %q, want digest username", authorization)
	}
}

func TestGetDiagnosticsReportsNegotiatedProtocolWithoutSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"api":"2.0.0","server":"2.1.3","text":"PrusaLink","hostname":"core-one-l","firmware":"6.10.1","printer":"1.4.0","capabilities":{"upload-by-put":true}}`)
	}))
	defer server.Close()

	client, err := NewPrusaLinkClientWithOptions(PrusaLinkClientOptions{
		BaseURL:        server.URL,
		APIKey:         "api-secret",
		DigestUsername: "maker",
		DigestPassword: "digest-secret",
		Timeout:        5,
	})
	if err != nil {
		t.Fatalf("NewPrusaLinkClientWithOptions() error = %v", err)
	}

	diagnostics, err := client.GetDiagnostics()
	if err != nil {
		t.Fatalf("GetDiagnostics() error = %v", err)
	}
	if diagnostics.BaseURL != server.URL || diagnostics.API != "2.0.0" || diagnostics.Firmware != "6.10.1" {
		t.Fatalf("GetDiagnostics() = %+v", diagnostics)
	}
	if len(diagnostics.Authentication) != 2 || diagnostics.Authentication[0] != "api-key" || diagnostics.Authentication[1] != "digest" {
		t.Fatalf("Authentication = %#v, want api-key and digest", diagnostics.Authentication)
	}
	formatted := fmt.Sprintf("%+v", diagnostics)
	if strings.Contains(formatted, "api-secret") || strings.Contains(formatted, "digest-secret") {
		t.Fatalf("diagnostics leaked credentials: %s", formatted)
	}
}

func TestFileDownloadUsesConfiguredDigestAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Digest ") {
			w.Header().Set("WWW-Authenticate", `Digest realm="PrusaLink", nonce="download-nonce", algorithm=MD5, qop="auth"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "; filament used [g] = 3.25")
	}))
	defer server.Close()

	client, err := NewPrusaLinkClientWithOptions(PrusaLinkClientOptions{
		BaseURL:             server.URL,
		DigestUsername:      "maker",
		DigestPassword:      "secret",
		Timeout:             5,
		FileDownloadTimeout: 5,
	})
	if err != nil {
		t.Fatalf("NewPrusaLinkClientWithOptions() error = %v", err)
	}

	usage, err := client.GetFilamentUsageFromDownloadWithRetry("usb/test.gcode", "/download", 5)
	if err != nil {
		t.Fatalf("GetFilamentUsageFromDownloadWithRetry() error = %v", err)
	}
	if usage[0] != 3.25 {
		t.Fatalf("usage = %+v, want tool 0 = 3.25", usage)
	}
}

func TestFileDownloadDoesNotSendCredentialsToCrossOriginReference(t *testing.T) {
	attackerRequests := 0
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attackerRequests++
		_, _ = io.WriteString(w, "; filament used [g] = 99")
	}))
	defer attacker.Close()

	printer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "secret" {
			http.Error(w, "missing API key", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "; filament used [g] = 3.25")
	}))
	defer printer.Close()

	client, err := NewPrusaLinkClientWithOptions(PrusaLinkClientOptions{
		BaseURL:             printer.URL,
		APIKey:              "secret",
		Timeout:             5,
		FileDownloadTimeout: 5,
	})
	if err != nil {
		t.Fatalf("NewPrusaLinkClientWithOptions() error = %v", err)
	}

	usage, err := client.GetFilamentUsageFromDownloadWithRetry("usb/test.gcode", attacker.URL+"/steal", 5)
	if err != nil {
		t.Fatalf("GetFilamentUsageFromDownloadWithRetry() error = %v", err)
	}
	if attackerRequests != 0 {
		t.Fatalf("cross-origin requests = %d, want 0", attackerRequests)
	}
	if usage[0] != 3.25 {
		t.Fatalf("usage = %+v, want trusted printer data", usage)
	}
}

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
		case "/api/v1/files/usb/test.gcode":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"meta":{},"refs":{"download":"/api/files/usb/test.gcode/raw"}}`)
		case "/api/files/usb/test.gcode/raw":
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

	usage, err := client.GetFilamentUsageForFile("usb/test.gcode")
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
