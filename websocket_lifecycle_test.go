package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWebServerShutdownStopsHubAndDisconnectsClients(t *testing.T) {
	_, web := newSecurityTestServer(t, "127.0.0.1", "", "")
	httpServer := httptest.NewServer(web.router)
	defer httpServer.Close()

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws/status"
	header := map[string][]string{"Origin": {httpServer.URL}}
	connection, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("connect WebSocket: %v", err)
	}
	defer connection.Close()

	web.Shutdown()
	web.Shutdown() // Shutdown is deliberately idempotent.

	select {
	case <-web.wsHub.done:
	case <-time.After(time.Second):
		t.Fatal("WebSocket hub did not stop")
	}
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	if _, _, err := connection.ReadMessage(); err == nil {
		t.Fatal("active WebSocket remained connected after shutdown")
	}
}

func TestWebSocketHubConcurrentBroadcastAndStop(t *testing.T) {
	_, web := newSecurityTestServer(t, "127.0.0.1", "", "")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			web.wsHub.publish([]byte("status"))
		}
	}()
	web.Shutdown()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publisher blocked while hub stopped")
	}
}
