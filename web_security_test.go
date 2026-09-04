package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func newSecurityTestServer(t *testing.T, host, username, password string) (*FilamentBridge, *WebServer) {
	t.Helper()
	t.Setenv("FILABRIDGE_WEB_USERNAME", username)
	t.Setenv("FILABRIDGE_WEB_PASSWORD", password)
	t.Setenv("FILABRIDGE_PUBLIC_ORIGIN", "")

	spoolman := newHistoryTestSpoolmanServer()
	t.Cleanup(spoolman.close)
	bridge := newTestBridge(t, spoolman.server.URL)
	web := NewWebServerForHost(bridge, host)
	t.Cleanup(web.Shutdown)
	return bridge, web
}

func requestRouter(t *testing.T, server *WebServer, method, path, body, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if username != "" || password != "" {
		request.SetBasicAuth(username, password)
	}
	server.router.ServeHTTP(recorder, request)
	return recorder
}

func TestManagementRoutesRequireConfiguredBasicAuth(t *testing.T) {
	_, server := newSecurityTestServer(t, "0.0.0.0", "operator", "correct horse")

	unauthorized := requestRouter(t, server, http.MethodGet, "/api/config", "", "", "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /api/config status = %d, want 401", unauthorized.Code)
	}
	authorized := requestRouter(t, server, http.MethodGet, "/api/config", "", "operator", "correct horse")
	if authorized.Code != http.StatusOK {
		t.Fatalf("authenticated GET /api/config status = %d, want 200: %s", authorized.Code, authorized.Body.String())
	}
	dashboard := requestRouter(t, server, http.MethodGet, "/", "", "", "")
	if dashboard.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated dashboard status = %d, want 401", dashboard.Code)
	}

	status := requestRouter(t, server, http.MethodGet, "/api/status", "", "", "")
	if status.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /api/status status = %d, want 401: %s", status.Code, status.Body.String())
	}
	authorizedStatus := requestRouter(t, server, http.MethodGet, "/api/status", "", "operator", "correct horse")
	if authorizedStatus.Code != http.StatusOK {
		t.Fatalf("authenticated GET /api/status status = %d, want 200: %s", authorizedStatus.Code, authorizedStatus.Body.String())
	}
}

func TestHealthzIsUnauthenticatedAndTopologyFree(t *testing.T) {
	t.Setenv("FILABRIDGE_WEB_USERNAME", "operator")
	t.Setenv("FILABRIDGE_WEB_PASSWORD", "correct horse")

	server := NewWebServerForHost(nil, "0.0.0.0")
	t.Cleanup(server.Shutdown)
	response := requestRouter(t, server, http.MethodGet, "/healthz", "", "", "")

	if response.Code != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if got, want := response.Header().Get("Content-Type"), "application/json; charset=utf-8"; got != want {
		t.Fatalf("GET /healthz Content-Type = %q, want %q", got, want)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /healthz response: %v", err)
	}
	if len(body) != 1 || body["status"] != "ok" {
		t.Fatalf("GET /healthz body = %#v, want topology-free status only", body)
	}
}

func TestManagementRoutesFailClosedWithoutCredentialsOffLoopback(t *testing.T) {
	_, server := newSecurityTestServer(t, "0.0.0.0", "", "")
	response := requestRouter(t, server, http.MethodGet, "/api/config", "", "", "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/config status = %d, want 503 when non-loopback auth is missing", response.Code)
	}

	_, loopbackServer := newSecurityTestServer(t, "127.0.0.1", "", "")
	response = requestRouter(t, loopbackServer, http.MethodGet, "/api/config", "", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("loopback GET /api/config status = %d, want 200: %s", response.Code, response.Body.String())
	}

	_, partialServer := newSecurityTestServer(t, "127.0.0.1", "operator", "")
	response = requestRouter(t, partialServer, http.MethodGet, "/api/config", "", "", "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("partially configured loopback auth status = %d, want 503", response.Code)
	}
}

func TestConfigInterfaceRedactsAndSafelyRotatesSpoolmanPassword(t *testing.T) {
	bridge, server := newSecurityTestServer(t, "0.0.0.0", "operator", "correct horse")
	if err := bridge.SetConfigValue(ConfigKeySpoolmanUsername, "spool-user"); err != nil {
		t.Fatal(err)
	}
	if err := bridge.SetConfigValue(ConfigKeySpoolmanPassword, "spool-secret"); err != nil {
		t.Fatal(err)
	}
	if err := bridge.ReloadConfig(); err != nil {
		t.Fatal(err)
	}

	response := requestRouter(t, server, http.MethodGet, "/api/config", "", "operator", "correct horse")
	var publicConfig map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &publicConfig); err != nil {
		t.Fatalf("decode config response: %v", err)
	}
	if _, exposed := publicConfig[ConfigKeySpoolmanPassword]; exposed {
		t.Fatalf("GET /api/config exposed %s", ConfigKeySpoolmanPassword)
	}
	if publicConfig["spoolman_password_configured"] != true {
		t.Fatalf("spoolman_password_configured = %#v, want true", publicConfig["spoolman_password_configured"])
	}

	blank := requestRouter(t, server, http.MethodPost, "/api/config", `{"spoolman_password":""}`, "operator", "correct horse")
	if blank.Code != http.StatusOK {
		t.Fatalf("blank password update status = %d: %s", blank.Code, blank.Body.String())
	}
	password, err := bridge.GetConfigValue(ConfigKeySpoolmanPassword)
	if err != nil || password != "spool-secret" {
		t.Fatalf("blank update stored password = %q, err=%v", password, err)
	}

	retarget := requestRouter(t, server, http.MethodPost, "/api/config", `{"spoolman_url":"http://attacker.invalid","spoolman_password":""}`, "operator", "correct horse")
	if retarget.Code != http.StatusBadRequest {
		t.Fatalf("credential-retargeting update status = %d, want 400", retarget.Code)
	}

	clear := requestRouter(t, server, http.MethodPost, "/api/config", `{"spoolman_url":"http://spoolman.internal","clear_spoolman_password":true}`, "operator", "correct horse")
	if clear.Code != http.StatusOK {
		t.Fatalf("explicit password clear status = %d: %s", clear.Code, clear.Body.String())
	}
	password, err = bridge.GetConfigValue(ConfigKeySpoolmanPassword)
	if err != nil || password != "" {
		t.Fatalf("explicit clear stored password = %q, err=%v", password, err)
	}
}

func TestConfigUpdateIsTypedValidatedAndAtomic(t *testing.T) {
	bridge, server := newSecurityTestServer(t, "0.0.0.0", "operator", "correct horse")
	originalURL, err := bridge.GetConfigValue(ConfigKeySpoolmanURL)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"not_a_setting":"value"}`},
		{name: "zero poll interval", body: `{"poll_interval":0}`},
		{name: "invalid URL", body: `{"spoolman_url":"file:///etc/passwd"}`},
		{name: "invalid update remains atomic", body: `{"spoolman_url":"http://changed.invalid","poll_interval":0}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := requestRouter(t, server, http.MethodPost, "/api/config", tt.body, "operator", "correct horse")
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", response.Code, response.Body.String())
			}
		})
	}
	storedURL, err := bridge.GetConfigValue(ConfigKeySpoolmanURL)
	if err != nil || storedURL != originalURL {
		t.Fatalf("invalid update changed URL to %q, want %q (err=%v)", storedURL, originalURL, err)
	}
}

func TestAuthenticatedManagementMutationRejectsCrossOriginBrowser(t *testing.T) {
	_, server := newSecurityTestServer(t, "0.0.0.0", "operator", "correct horse")
	body := `{"poll_interval":30}`

	crossOrigin := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(body))
	crossOrigin.Header.Set("Content-Type", "application/json")
	crossOrigin.Header.Set("Origin", "http://evil.invalid")
	crossOrigin.SetBasicAuth("operator", "correct horse")
	recorder := httptest.NewRecorder()
	server.router.ServeHTTP(recorder, crossOrigin)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin management POST status = %d, want 403", recorder.Code)
	}

	sameOrigin := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(body))
	sameOrigin.Header.Set("Content-Type", "application/json")
	sameOrigin.Header.Set("Origin", "http://example.com")
	sameOrigin.SetBasicAuth("operator", "correct horse")
	recorder = httptest.NewRecorder()
	server.router.ServeHTTP(recorder, sameOrigin)
	if recorder.Code != http.StatusOK {
		t.Fatalf("same-origin management POST status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}

	fetchMetadata := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(body))
	fetchMetadata.Header.Set("Content-Type", "application/json")
	fetchMetadata.Header.Set("Sec-Fetch-Site", "cross-site")
	fetchMetadata.SetBasicAuth("operator", "correct horse")
	recorder = httptest.NewRecorder()
	server.router.ServeHTTP(recorder, fetchMetadata)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-site fetch metadata POST status = %d, want 403", recorder.Code)
	}
}

func TestConfiguredPublicOriginSupportsTLSReverseProxy(t *testing.T) {
	_, server := newSecurityTestServer(t, "0.0.0.0", "operator", "correct horse")
	t.Setenv("FILABRIDGE_PUBLIC_ORIGIN", "https://filabridge.example")
	body := `{"poll_interval":30}`

	proxied := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(body))
	proxied.Header.Set("Content-Type", "application/json")
	proxied.Header.Set("Origin", "https://filabridge.example")
	proxied.SetBasicAuth("operator", "correct horse")
	recorder := httptest.NewRecorder()
	server.router.ServeHTTP(recorder, proxied)
	if recorder.Code != http.StatusOK {
		t.Fatalf("proxied HTTPS mutation status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}

	insecure := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(body))
	insecure.Header.Set("Content-Type", "application/json")
	insecure.Header.Set("Origin", "http://filabridge.example")
	insecure.SetBasicAuth("operator", "correct horse")
	recorder = httptest.NewRecorder()
	server.router.ServeHTTP(recorder, insecure)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("insecure public origin status = %d, want 403", recorder.Code)
	}

	websocketRequest := httptest.NewRequest(http.MethodGet, "http://filabridge:5000/ws/status", nil)
	websocketRequest.Header.Set("Origin", "https://filabridge.example")
	if !websocketOriginMatches(websocketRequest) {
		t.Fatal("configured HTTPS public origin did not authorize WebSocket origin")
	}
}

func TestNFCURLsUseConfiguredPublicOrigin(t *testing.T) {
	_, server := newSecurityTestServer(t, "0.0.0.0", "operator", "correct horse")
	t.Setenv("FILABRIDGE_PUBLIC_ORIGIN", "https://filabridge.example/")

	response := requestRouter(t, server, http.MethodGet, "/api/nfc/urls", "", "operator", "correct horse")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/nfc/urls status = %d, want 200: %s", response.Code, response.Body.String())
	}
	var payload struct {
		URLs []struct {
			URL string `json:"url"`
		} `json:"urls"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode NFC URLs: %v", err)
	}
	if len(payload.URLs) == 0 {
		t.Fatal("GET /api/nfc/urls returned no URLs")
	}
	for _, generated := range payload.URLs {
		if !strings.HasPrefix(generated.URL, "https://filabridge.example/api/nfc/assign?") {
			t.Fatalf("NFC URL = %q, want configured HTTPS public origin", generated.URL)
		}
	}
}

func TestWebSocketRequiresSameOrigin(t *testing.T) {
	_, server := newSecurityTestServer(t, "127.0.0.1", "", "")
	httpServer := httptest.NewServer(server.router)
	defer httpServer.Close()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws/status"

	header := http.Header{"Origin": []string{"http://evil.invalid"}}
	connection, response, err := websocket.DefaultDialer.Dial(wsURL, header)
	if connection != nil {
		_ = connection.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin WebSocket err=%v response=%v, want HTTP 403", err, response)
	}

	header.Set("Origin", httpServer.URL)
	connection, response, err = websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("same-origin WebSocket failed: response=%v err=%v", response, err)
	}
	_ = connection.Close()
}

func TestWebSocketRequiresManagementAuthOffLoopback(t *testing.T) {
	_, server := newSecurityTestServer(t, "0.0.0.0", "operator", "correct horse")
	request := httptest.NewRequest(http.MethodGet, "/ws/status", nil)
	request.Header.Set("Connection", "upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	request.Header.Set("Origin", "http://example.com")
	recorder := httptest.NewRecorder()
	server.router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated WebSocket status = %d, want 401", recorder.Code)
	}
}

func TestRouterHasSecurityHeadersAndNoProductionDebitSimulator(t *testing.T) {
	_, server := newSecurityTestServer(t, "127.0.0.1", "", "")
	response := requestRouter(t, server, http.MethodGet, "/", "", "", "")
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("dashboard response has no Content-Security-Policy")
	}
	if response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", response.Header().Get("X-Content-Type-Options"))
	}

	response = requestRouter(t, server, http.MethodPost, "/api/test/print_complete", `{}`, "", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("POST /api/test/print_complete status = %d, want 404", response.Code)
	}
}
