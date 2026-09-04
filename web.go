package main

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/skip2/go-qrcode"
)

//go:embed templates/*
var templatesFS embed.FS

//go:embed static/**
var staticFS embed.FS

// WebServer handles HTTP requests using Gin
type WebServer struct {
	bridge         *FilamentBridge
	router         *gin.Engine
	managementHost string
	operationMutex sync.Mutex // Protects add/update/delete printer operations
	wsHub          *WebSocketHub
}

// WebSocketHub manages WebSocket connections and broadcasts
type WebSocketHub struct {
	clients    map[*WebSocketClient]bool
	register   chan *WebSocketClient
	unregister chan *WebSocketClient
	broadcast  chan []byte
	stop       chan struct{}
	done       chan struct{}
	stopOnce   sync.Once
}

// WebSocketClient represents a WebSocket connection
type WebSocketClient struct {
	hub  *WebSocketHub
	conn *websocket.Conn
	send chan []byte
}

// WebSocketMessage represents the structure of messages sent to clients
type WebSocketMessage struct {
	Type             string                             `json:"type"`
	Timestamp        time.Time                          `json:"timestamp"`
	Printers         map[string]PrinterData             `json:"printers"`
	Spools           []SpoolmanSpool                    `json:"spools"`
	ToolheadMappings map[string]map[int]ToolheadMapping `json:"toolhead_mappings"`
	PrintErrors      []PrintError                       `json:"print_errors,omitempty"`
}

// NewWebServer creates a new web server with Gin
func NewWebServer(bridge *FilamentBridge) *WebServer {
	return NewWebServerForHost(bridge, "127.0.0.1")
}

// NewWebServerForHost binds management access policy to the actual listener
// host. Callers that expose FilaBridge beyond loopback must pass that host.
func NewWebServerForHost(bridge *FilamentBridge, host string) *WebServer {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()

	// Add middleware
	router.Use(gin.Logger())
	router.Use(gin.Recovery())
	router.Use(securityHeadersMiddleware())

	// Add custom recovery middleware for API routes to ensure JSON responses
	router.Use(func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				// Check if this is an API route
				if strings.HasPrefix(c.Request.URL.Path, "/api/") {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
					c.Abort()
				} else {
					// For non-API routes, use default recovery behavior
					c.AbortWithStatus(http.StatusInternalServerError)
				}
			}
		}()
		c.Next()
	})

	// Create WebSocket hub
	wsHub := newWebSocketHub()

	ws := &WebServer{
		bridge:         bridge,
		router:         router,
		managementHost: host,
		wsHub:          wsHub,
	}

	ws.setupRoutes()
	return ws
}

func newWebSocketHub() *WebSocketHub {
	hub := &WebSocketHub{
		clients:    make(map[*WebSocketClient]bool),
		register:   make(chan *WebSocketClient),
		unregister: make(chan *WebSocketClient),
		broadcast:  make(chan []byte, 1),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	go hub.run()
	return hub
}

// generateToolheadIDs generates a slice of toolhead IDs from 0 to count-1
func generateToolheadIDs(count int) []int {
	ids := make([]int, count)
	for i := 0; i < count; i++ {
		ids[i] = i
	}
	return ids
}

// setupRoutes configures all the routes
func (ws *WebServer) setupRoutes() {
	// Load HTML templates with custom functions from embedded filesystem
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"generateToolheadIDs":   generateToolheadIDs,
		"formatDurationSeconds": formatDurationSeconds,
		"formatETASeconds":      formatETASeconds,
	}).ParseFS(templatesFS, "templates/*"))
	ws.router.SetHTMLTemplate(tmpl)

	// Static files (embedded in binary)
	// Use fs.Sub to strip the "static/" prefix from embedded paths
	staticSubFS, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("Failed to create static filesystem: %v", err)
	}
	ws.router.StaticFS("/static", http.FS(staticSubFS))

	// Process readiness only. Keep this route independent from printers,
	// Spoolman, SQLite queries, and management credentials.
	ws.router.GET("/healthz", healthzHandler)

	// Main dashboard
	ws.router.GET("/", managementAccessMiddleware(ws.managementHost), ws.dashboardHandler)

	// API routes
	api := ws.router.Group("/api")

	// Status, inventory, configuration, and every state-changing operation share
	// one authenticated management seam.
	management := api.Group("")
	management.Use(managementAccessMiddleware(ws.managementHost), managementOriginMiddleware())
	{
		management.GET("/status", ws.statusHandler)
		management.GET("/spools", ws.spoolsHandler)
		management.GET("/print-history", ws.getPrintHistoryHandler)
		management.POST("/print-history/import", ws.importPrintHistoryHandler)
		management.PUT("/print-history/:id", ws.updatePrintHistoryHandler)
		management.PUT("/print-history/:id/spool", ws.updatePrintHistoryHandler)
		management.POST("/print-history/:id/pull", ws.pullPrintHistoryHandler)
		management.GET("/filaments", ws.filamentsHandler)
		management.POST("/map_toolhead", ws.mapToolheadHandler)
		management.GET("/available_spools", ws.availableSpoolsHandler)
		management.GET("/spoolman/test", ws.testSpoolmanConnectionHandler)
		management.GET("/spoolman/debug", ws.debugSpoolmanHandler)
		management.GET("/spoolman/capabilities", ws.spoolmanCapabilitiesHandler)
		management.GET("/spoolman/tags/:uid", ws.lookupSpoolmanTagHandler)
		management.POST("/spools/:id/tags", ws.associateSpoolmanTagHandler)
		management.GET("/spools/:id/consumption-authority", ws.getSpoolConsumptionAuthorityHandler)
		management.PUT("/spools/:id/consumption-authority", ws.updateSpoolConsumptionAuthorityHandler)
		management.GET("/config", ws.getConfigHandler)
		management.POST("/config", ws.updateConfigHandler)
		management.GET("/config/auto-assign-previous-spool", ws.getAutoAssignPreviousSpoolHandler)
		management.PUT("/config/auto-assign-previous-spool", ws.updateAutoAssignPreviousSpoolHandler)
		management.GET("/printers", ws.getPrintersHandler)
		management.GET("/printer-presets", ws.printerPresetsHandler)
		management.GET("/prusaslicer/profiles.zip", requiredProfileExportAuthMiddleware(), ws.prusaSlicerProfilesHandler)
		management.POST("/printers", ws.addPrinterHandler)
		management.PUT("/printers/:id", ws.updatePrinterHandler)
		management.DELETE("/printers/:id", ws.deletePrinterHandler)
		management.GET("/printers/:id/toolheads", ws.getToolheadNamesHandler)
		management.PUT("/printers/:id/toolheads/:toolhead_id", ws.updateToolheadNameHandler)
		management.GET("/printers/:id/tool-routes", ws.getLogicalToolRoutesHandler)
		management.PUT("/printers/:id/tool-routes/:logical_tool_id", ws.updateLogicalToolRouteHandler)
		management.DELETE("/printers/:id/tool-routes/:logical_tool_id", ws.resetLogicalToolRouteHandler)
		management.GET("/printers/:id/prusalink-diagnostics", ws.prusaLinkDiagnosticsHandler)
		management.GET("/printers/:id/job-reconciliation", ws.getJobReconciliationHandler)
		management.POST("/printers/:id/job-reconciliation/resolve", ws.resolveJobReconciliationHandler)
		management.POST("/detect_printer", ws.detectPrinterHandler)
		management.GET("/print-errors", ws.getPrintErrorsHandler)
		management.POST("/print-errors/:id/acknowledge", ws.acknowledgePrintErrorHandler)
		management.GET("/nfc/assign", ws.nfcAssignConfirmationHandler)
		management.POST("/nfc/assign", ws.nfcAssignHandler)
		management.GET("/nfc/urls", ws.nfcUrlsHandler)
		management.GET("/nfc/session/status", ws.nfcSessionStatusHandler)
		management.GET("/locations", ws.getLocationsHandler)
		management.GET("/locations/:name/status", ws.getLocationStatusHandler)
		management.POST("/locations", ws.createLocationHandler)
		management.PUT("/locations/:name", ws.updateLocationHandler)
		management.DELETE("/locations/:name", ws.deleteLocationHandler)
	}

	// WebSocket endpoint
	ws.router.GET("/ws/status", managementAccessMiddleware(ws.managementHost), ws.websocketHandler)
}

func healthzHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func securityHeadersMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self' ws: wss:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		c.Next()
	}
}

func managementAccessMiddleware(host string) gin.HandlerFunc {
	username := os.Getenv("FILABRIDGE_WEB_USERNAME")
	password := os.Getenv("FILABRIDGE_WEB_PASSWORD")

	if username == "" && password == "" && isLoopbackHost(host) {
		return func(c *gin.Context) { c.Next() }
	}
	if username == "" || password == "" {
		return func(c *gin.Context) {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error": "management authentication is required for non-loopback access",
			})
		}
	}

	// Gin owns parsing and constant-time verification of Basic credentials.
	return gin.BasicAuth(gin.Accounts{username: password})
}

func managementOriginMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}

		fetchSite := strings.ToLower(strings.TrimSpace(c.GetHeader("Sec-Fetch-Site")))
		if fetchSite == "cross-site" || fetchSite == "same-site" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "cross-origin management request rejected"})
			return
		}
		if origin := strings.TrimSpace(c.GetHeader("Origin")); origin != "" && !requestOriginMatches(c.Request, origin) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "cross-origin management request rejected"})
			return
		}
		c.Next()
	}
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func formatDurationSeconds(seconds int) string {
	if seconds <= 0 {
		return "0m"
	}

	hours := seconds / 3600
	minutes := (seconds % 3600) / 60

	if hours > 0 {
		return fmt.Sprintf("%dh %02dm", hours, minutes)
	}

	return fmt.Sprintf("%dm", minutes)
}

func formatETASeconds(seconds int) string {
	if seconds <= 0 {
		return "-"
	}

	eta := time.Now().Add(time.Duration(seconds) * time.Second)
	now := time.Now()

	if eta.Year() == now.Year() && eta.YearDay() == now.YearDay() {
		return eta.Format("15:04")
	}

	if eta.Year() == now.Year() && eta.YearDay() == now.YearDay()+1 {
		return "Tomorrow " + eta.Format("15:04")
	}

	return eta.Format("Jan 2 15:04")
}

// WebSocket hub methods

// run starts the WebSocket hub
func (h *WebSocketHub) run() {
	defer close(h.done)
	for {
		select {
		case client := <-h.register:
			h.clients[client] = true
			log.Printf("WebSocket client connected. Total clients: %d", len(h.clients))

		case client := <-h.unregister:
			h.removeOwnedClient(client)
			log.Printf("WebSocket client disconnected. Total clients: %d", len(h.clients))

		case message := <-h.broadcast:
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					h.removeOwnedClient(client)
				}
			}

		case <-h.stop:
			for client := range h.clients {
				h.removeOwnedClient(client)
			}
			return
		}
	}
}

func (h *WebSocketHub) removeOwnedClient(client *WebSocketClient) {
	if _, exists := h.clients[client]; !exists {
		return
	}
	delete(h.clients, client)
	close(client.send)
	if client.conn != nil {
		_ = client.conn.Close()
	}
}

func (h *WebSocketHub) registerClient(client *WebSocketClient) bool {
	select {
	case h.register <- client:
		return true
	case <-h.done:
		return false
	}
}

func (h *WebSocketHub) unregisterClient(client *WebSocketClient) {
	select {
	case h.unregister <- client:
	case <-h.done:
	}
}

func (h *WebSocketHub) publish(message []byte) bool {
	select {
	case h.broadcast <- message:
		return true
	case <-h.done:
		return false
	default:
		return false
	}
}

func (h *WebSocketHub) Stop() {
	h.stopOnce.Do(func() { close(h.stop) })
	<-h.done
}

// Shutdown terminates the hub and closes every active WebSocket connection.
func (ws *WebServer) Shutdown() {
	if ws == nil || ws.wsHub == nil {
		return
	}
	ws.wsHub.Stop()
}

// BroadcastStatus sends status updates to all connected clients
func (ws *WebServer) BroadcastStatus() {
	// Get current status
	status, err := ws.bridge.GetStatus()
	if err != nil {
		log.Printf("Error getting status for broadcast: %v", err)
		return
	}

	// Get current spools
	spools, err := ws.bridge.spoolmanSnapshot().GetAllSpools()
	if err != nil {
		log.Printf("Error getting spools for broadcast: %v", err)
		spools = []SpoolmanSpool{}
	}

	// Get print errors
	printErrors := ws.bridge.GetPrintErrors()

	// Create message
	message := WebSocketMessage{
		Type:             "status_update",
		Timestamp:        time.Now(),
		Printers:         status.Printers,
		Spools:           spools,
		ToolheadMappings: status.ToolheadMappings,
		PrintErrors:      printErrors,
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(message)
	if err != nil {
		log.Printf("Error marshaling WebSocket message: %v", err)
		return
	}

	// Broadcast to all clients
	if !ws.wsHub.publish(jsonData) {
		log.Printf("WebSocket status update coalesced or hub stopped")
	}
}

// websocketHandler handles WebSocket connections
func (ws *WebServer) websocketHandler(c *gin.Context) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return websocketOriginMatches(r)
		},
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	client := &WebSocketClient{
		hub:  ws.wsHub,
		conn: conn,
		send: make(chan []byte, 256),
	}

	if !client.hub.registerClient(client) {
		_ = conn.Close()
		return
	}

	// Start goroutines for reading and writing
	go client.writePump()
	go client.readPump()
}

func websocketOriginMatches(request *http.Request) bool {
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	return requestOriginMatches(request, origin)
}

func requestOriginMatches(request *http.Request, origin string) bool {
	parsed, err := neturl.Parse(origin)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return false
	}
	actualOrigin := strings.ToLower(parsed.Scheme + "://" + parsed.Host)

	expectedOrigin, err := publicOriginForRequest(request)
	return err == nil && actualOrigin == expectedOrigin
}

func publicOriginForRequest(request *http.Request) (string, error) {
	if configuredOrigin := strings.TrimSpace(os.Getenv("FILABRIDGE_PUBLIC_ORIGIN")); configuredOrigin != "" {
		origin, err := serviceOrigin(configuredOrigin)
		if err != nil {
			return "", fmt.Errorf("invalid FILABRIDGE_PUBLIC_ORIGIN: %w", err)
		}
		return origin, nil
	}

	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	origin, err := serviceOrigin(scheme + "://" + request.Host)
	if err != nil {
		return "", fmt.Errorf("invalid request origin: %w", err)
	}
	return origin, nil
}

// WebSocket client methods

// readPump pumps messages from the WebSocket connection to the hub
func (c *WebSocketClient) readPump() {
	defer func() {
		c.hub.unregisterClient(c)
		_ = c.conn.Close()
	}()

	c.conn.SetReadLimit(512)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}
	}
}

// writePump pumps messages from the hub to the WebSocket connection
func (c *WebSocketClient) writePump() {
	ticker := time.NewTicker(54 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			// Add queued chat messages to the current websocket message
			n := len(c.send)
			for i := 0; i < n; i++ {
				w.Write([]byte{'\n'})
				w.Write(<-c.send)
			}

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// dashboardHandler serves the main dashboard
func (ws *WebServer) dashboardHandler(c *gin.Context) {
	runtime := ws.bridge.runtimeSnapshot()
	status, err := ws.bridge.GetStatus()
	if err != nil {
		c.HTML(http.StatusInternalServerError, "error.html", gin.H{
			"Error": "Failed to get printer status",
		})
		return
	}

	// Test Spoolman connection
	spoolmanConnected := true
	spoolmanError := ""
	spools, err := runtime.spoolman.GetAllSpools()
	if err != nil {
		spoolmanConnected = false
		spoolmanError = err.Error()
		spools = []SpoolmanSpool{}
	}

	// Check if this is a first run
	isFirstRun, err := ws.bridge.IsFirstRun()
	if err != nil {
		isFirstRun = false
	}

	hasErrors := !spoolmanConnected || hasConnectionErrors(status)

	// Get print errors
	printErrors := ws.bridge.GetPrintErrors()
	hasPrintErrors := len(printErrors) > 0

	c.HTML(http.StatusOK, "index.html", gin.H{
		"Status":            status,
		"Spools":            spools,
		"HasErrors":         hasErrors,
		"HasPrintErrors":    hasPrintErrors,
		"PrintErrors":       printErrors,
		"IsFirstRun":        isFirstRun,
		"Printers":          runtime.config.Printers,
		"SpoolmanConnected": spoolmanConnected,
		"SpoolmanError":     spoolmanError,
		"SpoolmanBaseURL":   runtime.config.SpoolmanURL,
	})
}

// hasConnectionErrors checks if there are connection errors
func hasConnectionErrors(status *PrinterStatus) bool {
	for _, printer := range status.Printers {
		if printer.State == StateOffline {
			return true
		}
	}
	return false
}

// statusHandler returns current status as JSON
func (ws *WebServer) statusHandler(c *gin.Context) {
	status, err := ws.bridge.GetStatus()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, status)
}

// spoolsHandler returns all spools as JSON
func (ws *WebServer) spoolsHandler(c *gin.Context) {
	spoolman := ws.bridge.spoolmanSnapshot()
	includeEmpty := strings.EqualFold(c.DefaultQuery("include_empty", "false"), "true") || c.DefaultQuery("include_empty", "0") == "1"

	var (
		spools []SpoolmanSpool
		err    error
	)

	if includeEmpty {
		spools, err = spoolman.GetAllSpoolsIncludingEmpty()
	} else {
		spools, err = spoolman.GetAllSpools()
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, spools)
}

func (ws *WebServer) getPrintHistoryHandler(c *gin.Context) {
	limit := 200
	if limitParam := c.Query("limit"); limitParam != "" {
		parsedLimit, err := strconv.Atoi(limitParam)
		if err != nil || parsedLimit <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be a positive integer"})
			return
		}
		limit = parsedLimit
	}

	history, err := ws.bridge.GetPrintHistory(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"history": history})
}

func (ws *WebServer) updatePrintHistoryHandler(c *gin.Context) {
	historyID, err := strconv.Atoi(c.Param("id"))
	if err != nil || historyID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid print history id"})
		return
	}

	var req struct {
		SpoolID      *int     `json:"spool_id"`
		FilamentUsed *float64 `json:"filament_used"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON payload"})
		return
	}

	if req.SpoolID != nil && *req.SpoolID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "spool_id must be greater than 0"})
		return
	}

	entry, err := ws.bridge.getPrintHistoryByID(historyID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	filamentUsed := entry.FilamentUsed
	if req.FilamentUsed != nil {
		if *req.FilamentUsed < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "filament_used must be greater than or equal to 0"})
			return
		}
		filamentUsed = *req.FilamentUsed
	}

	if err := ws.bridge.UpdatePrintHistory(historyID, req.SpoolID, filamentUsed); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Print history updated successfully"})
}

func (ws *WebServer) pullPrintHistoryHandler(c *gin.Context) {
	historyID, err := strconv.Atoi(c.Param("id"))
	if err != nil || historyID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid print history id"})
		return
	}

	var req struct {
		SpoolID *int `json:"spool_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON payload"})
		return
	}

	if req.SpoolID != nil && *req.SpoolID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "spool_id must be greater than 0"})
		return
	}

	entry, err := ws.bridge.RefreshPrintHistoryFilamentUsage(historyID, req.SpoolID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Print history refreshed successfully",
		"entry":   entry,
	})
}

func (ws *WebServer) importPrintHistoryHandler(c *gin.Context) {
	var req struct {
		PrinterID         string `json:"printer_id" binding:"required"`
		DefaultToolheadID int    `json:"default_toolhead_id"`
		Payload           string `json:"payload" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON payload"})
		return
	}

	summary, err := ws.bridge.ImportPrusaConnectPrintHistory(req.PrinterID, req.DefaultToolheadID, []byte(req.Payload))
	if err != nil {
		var validationErr *printHistoryImportValidationError
		if errors.As(err, &validationErr) {
			c.JSON(http.StatusBadRequest, gin.H{"error": validationErr.Error()})
			return
		}

		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Print history imported successfully",
		"summary": summary,
	})
}

// filamentsHandler returns all filament types as JSON
func (ws *WebServer) filamentsHandler(c *gin.Context) {
	filaments, err := ws.bridge.spoolmanSnapshot().GetAllFilaments()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, filaments)
}

// validatePrinterConfig validates printer configuration input
func validatePrinterConfig(config PrinterConfig) error {
	if config.Name == "" {
		return fmt.Errorf("printer name is required")
	}
	if config.IPAddress == "" {
		return fmt.Errorf("address is required")
	}
	if config.Toolheads < 1 {
		return fmt.Errorf("toolheads must be at least 1")
	}
	if config.Toolheads > 10 {
		return fmt.Errorf("toolheads cannot exceed 10")
	}
	return nil
}

// validateAddress validates hostname or IP address format
func validateAddress(address string) error {
	if address == "" {
		return fmt.Errorf("address cannot be empty")
	}
	if strings.Contains(address, "://") {
		parsed, err := neturl.Parse(address)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("address must be an HTTP or HTTPS PrusaLink URL")
		}
		if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
			return fmt.Errorf("PrusaLink URL must not contain credentials, query, fragment, or path")
		}
		return nil
	}
	// Basic validation - check for reasonable length (hostnames can be longer than IPs)
	// Minimum: 1 character (e.g., "a"), Maximum: 253 characters (RFC 1035)
	if len(address) < 1 || len(address) > 253 {
		return fmt.Errorf("invalid address format")
	}
	// Basic character validation - allow common characters used in hostnames and IP addresses
	// This includes: letters, numbers, dots, hyphens, underscores, colons (for IPv6), and brackets (for IPv6)
	// The HTTP client will perform more thorough validation when connecting
	for _, char := range address {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '.' || char == '-' || char == '_' ||
			char == ':' || char == '[' || char == ']') {
			return fmt.Errorf("invalid address format: contains invalid characters")
		}
	}
	return nil
}

// mapToolheadHandler maps a spool to a toolhead
func (ws *WebServer) mapToolheadHandler(c *gin.Context) {
	var req struct {
		PrinterName           string `json:"printer_name" binding:"required"`
		ToolheadID            int    `json:"toolhead_id"`
		SpoolID               int    `json:"spool_id"`
		PreviousSpoolLocation string `json:"previous_spool_location"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON"})
		return
	}

	if req.PrinterName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing required parameters"})
		return
	}

	if req.ToolheadID < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Toolhead ID must be non-negative"})
		return
	}

	if err := ws.bridge.SwitchToolheadSpool(req.PrinterName, req.ToolheadID, req.SpoolID, req.PreviousSpoolLocation); err != nil {
		switch {
		case strings.Contains(err.Error(), "is already assigned to"):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		case strings.Contains(err.Error(), "needs a storage location"),
			strings.Contains(err.Error(), "previous spool location"),
			strings.Contains(err.Error(), "auto-assign previous spool location"):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	if req.SpoolID == 0 {
		c.JSON(http.StatusOK, gin.H{"message": "Toolhead unmapped successfully"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Toolhead mapped successfully"})
}

// availableSpoolsHandler returns spools available for assignment to a specific toolhead
func (ws *WebServer) availableSpoolsHandler(c *gin.Context) {
	printerName := c.Query("printer_name")
	toolheadIDStr := c.Query("toolhead_id")

	if printerName == "" || toolheadIDStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "printer_name and toolhead_id parameters are required"})
		return
	}

	toolheadID, err := strconv.Atoi(toolheadIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid toolhead_id"})
		return
	}

	// Get all spools from Spoolman
	allSpools, err := ws.bridge.spoolmanSnapshot().GetAllSpools()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Get all current toolhead mappings
	allMappings, err := ws.bridge.GetAllToolheadMappings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Create a set of assigned spool IDs (excluding the current toolhead)
	assignedSpoolIDs := make(map[int]bool)
	for _, printerMappings := range allMappings {
		for tid, mapping := range printerMappings {
			// Skip the current toolhead (allow re-assignment to the same toolhead)
			if mapping.PrinterName == printerName && tid == toolheadID {
				continue
			}
			// Mark this spool as assigned (prevents same spool being used on multiple printers)
			assignedSpoolIDs[mapping.SpoolID] = true
		}
	}

	// Filter out assigned spools
	var availableSpools []SpoolmanSpool
	for _, spool := range allSpools {
		if !assignedSpoolIDs[spool.ID] {
			availableSpools = append(availableSpools, spool)
		}
	}

	c.JSON(http.StatusOK, gin.H{"spools": availableSpools})
}

// getConfigHandler returns current configuration
func (ws *WebServer) getConfigHandler(c *gin.Context) {
	config, err := ws.bridge.GetAllConfig()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		ConfigKeySpoolmanURL:                     config[ConfigKeySpoolmanURL],
		ConfigKeySpoolmanUsername:                config[ConfigKeySpoolmanUsername],
		"spoolman_password_configured":           config[ConfigKeySpoolmanPassword] != "",
		ConfigKeyConsumptionAuthority:            config[ConfigKeyConsumptionAuthority],
		ConfigKeyPollInterval:                    config[ConfigKeyPollInterval],
		ConfigKeyLocationSyncInterval:            config[ConfigKeyLocationSyncInterval],
		ConfigKeyWebPort:                         config[ConfigKeyWebPort],
		ConfigKeyPrusaLinkTimeout:                config[ConfigKeyPrusaLinkTimeout],
		ConfigKeyPrusaLinkFileDownloadTimeout:    config[ConfigKeyPrusaLinkFileDownloadTimeout],
		ConfigKeySpoolmanTimeout:                 config[ConfigKeySpoolmanTimeout],
		ConfigKeyAutoAssignPreviousSpoolEnabled:  config[ConfigKeyAutoAssignPreviousSpoolEnabled],
		ConfigKeyAutoAssignPreviousSpoolLocation: config[ConfigKeyAutoAssignPreviousSpoolLocation],
	})
}

type configUpdateRequest struct {
	SpoolmanURL                  *string               `json:"spoolman_url"`
	SpoolmanUsername             *string               `json:"spoolman_username"`
	SpoolmanPassword             *string               `json:"spoolman_password"`
	ClearSpoolmanPassword        bool                  `json:"clear_spoolman_password"`
	ConsumptionAuthority         *ConsumptionAuthority `json:"consumption_authority"`
	PollInterval                 *int                  `json:"poll_interval"`
	LocationSyncInterval         *int                  `json:"location_sync_interval"`
	WebPort                      *int                  `json:"web_port"`
	PrusaLinkTimeout             *int                  `json:"prusalink_timeout"`
	PrusaLinkFileDownloadTimeout *int                  `json:"prusalink_file_download_timeout"`
	SpoolmanTimeout              *int                  `json:"spoolman_timeout"`
}

// updateConfigHandler updates configuration
func (ws *WebServer) updateConfigHandler(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var request configUpdateRequest
	if err := decoder.Decode(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid configuration payload: " + err.Error()})
		return
	}
	if err := ensureJSONDocumentEnded(decoder); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	current, err := ws.bridge.GetAllConfig()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	updates, err := validatedConfigUpdates(request, current)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(updates) == 0 {
		if request.SpoolmanPassword != nil && *request.SpoolmanPassword == "" {
			c.JSON(http.StatusOK, gin.H{"message": "Configuration updated successfully"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "configuration update contains no changes"})
		return
	}
	if err := ws.persistConfigUpdates(updates); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Reload configuration
	newConfig, err := LoadConfig(ws.bridge)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := ws.bridge.UpdateConfig(newConfig); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Configuration updated successfully"})
}

func ensureJSONDocumentEnded(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("configuration payload must contain exactly one JSON object")
		}
		return fmt.Errorf("invalid configuration payload: %w", err)
	}
	return nil
}

func validatedConfigUpdates(request configUpdateRequest, current map[string]string) (map[string]string, error) {
	updates := make(map[string]string)
	if request.ClearSpoolmanPassword && request.SpoolmanPassword != nil && *request.SpoolmanPassword != "" {
		return nil, fmt.Errorf("spoolman_password and clear_spoolman_password cannot be used together")
	}

	if request.SpoolmanURL != nil {
		spoolmanURL, err := validateServiceBaseURL(*request.SpoolmanURL)
		if err != nil {
			return nil, fmt.Errorf("invalid Spoolman URL: %w", err)
		}
		currentOrigin, _ := serviceOrigin(current[ConfigKeySpoolmanURL])
		newOrigin, _ := serviceOrigin(spoolmanURL)
		passwordReentered := request.SpoolmanPassword != nil && *request.SpoolmanPassword != ""
		if current[ConfigKeySpoolmanPassword] != "" && currentOrigin != newOrigin && !passwordReentered && !request.ClearSpoolmanPassword {
			return nil, fmt.Errorf("changing the Spoolman origin requires re-entering or explicitly clearing its password")
		}
		updates[ConfigKeySpoolmanURL] = spoolmanURL
	}
	if request.SpoolmanUsername != nil {
		updates[ConfigKeySpoolmanUsername] = strings.TrimSpace(*request.SpoolmanUsername)
	}
	if request.ClearSpoolmanPassword {
		updates[ConfigKeySpoolmanPassword] = ""
	} else if request.SpoolmanPassword != nil && *request.SpoolmanPassword != "" {
		updates[ConfigKeySpoolmanPassword] = *request.SpoolmanPassword
	}
	if request.ConsumptionAuthority != nil {
		authority, err := ParseConsumptionAuthority(string(*request.ConsumptionAuthority))
		if err != nil {
			return nil, err
		}
		updates[ConfigKeyConsumptionAuthority] = string(authority)
	}
	if err := addBoundedConfigInt(updates, ConfigKeyPollInterval, request.PollInterval, 10, 300); err != nil {
		return nil, err
	}
	if err := addBoundedConfigInt(updates, ConfigKeyLocationSyncInterval, request.LocationSyncInterval, 1, 1440); err != nil {
		return nil, err
	}
	if err := addBoundedConfigInt(updates, ConfigKeyWebPort, request.WebPort, 1, 65535); err != nil {
		return nil, err
	}
	if err := addBoundedConfigInt(updates, ConfigKeyPrusaLinkTimeout, request.PrusaLinkTimeout, 5, 300); err != nil {
		return nil, err
	}
	if err := addBoundedConfigInt(updates, ConfigKeyPrusaLinkFileDownloadTimeout, request.PrusaLinkFileDownloadTimeout, 10, 600); err != nil {
		return nil, err
	}
	if err := addBoundedConfigInt(updates, ConfigKeySpoolmanTimeout, request.SpoolmanTimeout, 5, 300); err != nil {
		return nil, err
	}

	return updates, nil
}

func addBoundedConfigInt(updates map[string]string, key string, value *int, minimum, maximum int) error {
	if value == nil {
		return nil
	}
	if *value < minimum || *value > maximum {
		return fmt.Errorf("%s must be between %d and %d", key, minimum, maximum)
	}
	updates[key] = strconv.Itoa(*value)
	return nil
}

func validateServiceBaseURL(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := neturl.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("must be an absolute HTTP or HTTPS URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return "", fmt.Errorf("must not contain credentials, query, fragment, or path")
	}
	return value, nil
}

func serviceOrigin(value string) (string, error) {
	validated, err := validateServiceBaseURL(value)
	if err != nil {
		return "", err
	}
	parsed, err := neturl.Parse(validated)
	if err != nil {
		return "", err
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host), nil
}

func (ws *WebServer) persistConfigUpdates(updates map[string]string) error {
	tx, err := ws.bridge.db.Begin()
	if err != nil {
		return fmt.Errorf("begin configuration update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	keys := make([]string, 0, len(updates))
	for key := range updates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := tx.Exec(`
			INSERT INTO configuration (key, value, updated_at)
			VALUES (?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
		`, key, updates[key]); err != nil {
			return fmt.Errorf("save configuration key %s: %w", key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit configuration update: %w", err)
	}
	return nil
}

// getAutoAssignPreviousSpoolHandler returns current auto-assign previous spool settings
func (ws *WebServer) getAutoAssignPreviousSpoolHandler(c *gin.Context) {
	enabled, err := ws.bridge.GetAutoAssignPreviousSpoolEnabled()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	location, err := ws.bridge.GetAutoAssignPreviousSpoolLocation()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"enabled":  enabled,
		"location": location,
	})
}

// updateAutoAssignPreviousSpoolHandler updates auto-assign previous spool settings
func (ws *WebServer) updateAutoAssignPreviousSpoolHandler(c *gin.Context) {
	var req struct {
		Enabled  bool   `json:"enabled" binding:"required"`
		Location string `json:"location"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON or missing 'enabled' field"})
		return
	}

	// Update enabled setting
	if err := ws.bridge.SetAutoAssignPreviousSpoolEnabled(req.Enabled); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Update location setting
	if err := ws.bridge.SetAutoAssignPreviousSpoolLocation(req.Location); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Auto-assign previous spool settings updated successfully"})
}

// getPrintersHandler returns all configured printers
func (ws *WebServer) getPrintersHandler(c *gin.Context) {
	printerConfigs, err := ws.bridge.GetAllPrinterConfigs()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Enhance printer configs with toolhead names
	result := make(map[string]interface{})
	for printerID, printerConfig := range printerConfigs {
		printerData := map[string]interface{}{
			"name":                           printerConfig.Name,
			"preset_id":                      resolvedPrinterPresetID(printerConfig.Model, printerConfig.Toolheads),
			"model":                          printerConfig.Model,
			"ip_address":                     printerConfig.IPAddress,
			"api_key_configured":             printerConfig.APIKey != "",
			"prusalink_username":             printerConfig.PrusaLinkUsername,
			"prusalink_password_configured":  printerConfig.PrusaLinkPassword != "",
			"prusalink_custom_ca_configured": printerConfig.PrusaLinkCustomCAPEM != "",
			"toolheads":                      printerConfig.Toolheads,
		}

		// Get toolhead names for this printer
		toolheadNames, err := ws.bridge.GetAllToolheadNames(printerID)
		if err == nil {
			// Build toolhead names map with defaults
			toolheadNamesMap := make(map[int]string)
			for toolheadID := 0; toolheadID < printerConfig.Toolheads; toolheadID++ {
				if name, exists := toolheadNames[toolheadID]; exists {
					toolheadNamesMap[toolheadID] = name
				} else {
					toolheadNamesMap[toolheadID] = fmt.Sprintf("Toolhead %d", toolheadID)
				}
			}
			printerData["toolhead_names"] = toolheadNamesMap
		}

		result[printerID] = printerData
	}

	c.JSON(http.StatusOK, gin.H{"printers": result})
}

// printerPresetsHandler returns model/toolhead combinations supported by the
// settings UI. The custom ID keeps manual and third-party configs available.
func (ws *WebServer) printerPresetsHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"presets":          PrinterPresets(),
		"custom_preset_id": CustomPrinterPresetID,
	})
}

func (ws *WebServer) getLogicalToolRoutesHandler(c *gin.Context) {
	printerConfig, ok := ws.printerConfigByID(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "printer not found"})
		return
	}
	routes, err := ws.bridge.GetLogicalToolRoutes(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for logicalToolID := 0; logicalToolID < printerConfig.Toolheads; logicalToolID++ {
		if _, exists := routes[logicalToolID]; !exists {
			routes[logicalToolID] = logicalToolID
		}
	}
	c.JSON(http.StatusOK, gin.H{"routes": routes})
}

func (ws *WebServer) updateLogicalToolRouteHandler(c *gin.Context) {
	printerConfig, ok := ws.printerConfigByID(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "printer not found"})
		return
	}
	logicalToolID, err := strconv.Atoi(c.Param("logical_tool_id"))
	if err != nil || logicalToolID < 0 || logicalToolID >= printerConfig.Toolheads {
		c.JSON(http.StatusBadRequest, gin.H{"error": "logical tool ID is outside printer range"})
		return
	}
	var request struct {
		PhysicalToolheadID int `json:"physical_toolhead_id"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON payload"})
		return
	}
	if request.PhysicalToolheadID < 0 || request.PhysicalToolheadID >= printerConfig.Toolheads {
		c.JSON(http.StatusBadRequest, gin.H{"error": "physical toolhead ID is outside printer range"})
		return
	}
	if err := ws.bridge.SetLogicalToolRoute(c.Param("id"), logicalToolID, request.PhysicalToolheadID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"logical_tool_id": logicalToolID, "physical_toolhead_id": request.PhysicalToolheadID})
}

func (ws *WebServer) resetLogicalToolRouteHandler(c *gin.Context) {
	printerConfig, ok := ws.printerConfigByID(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "printer not found"})
		return
	}
	logicalToolID, err := strconv.Atoi(c.Param("logical_tool_id"))
	if err != nil || logicalToolID < 0 || logicalToolID >= printerConfig.Toolheads {
		c.JSON(http.StatusBadRequest, gin.H{"error": "logical tool ID is outside printer range"})
		return
	}
	if err := ws.bridge.ResetLogicalToolRoute(c.Param("id"), logicalToolID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

func (ws *WebServer) printerConfigByID(printerID string) (PrinterConfig, bool) {
	configs, err := ws.bridge.GetAllPrinterConfigs()
	if err != nil {
		return PrinterConfig{}, false
	}
	config, ok := configs[printerID]
	return config, ok
}

func (ws *WebServer) prusaLinkDiagnosticsHandler(c *gin.Context) {
	printerConfig, ok := ws.printerConfigByID(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "printer not found"})
		return
	}
	settings := ws.bridge.GetConfigSnapshot()
	if settings == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration is unavailable"})
		return
	}
	client, err := newConfiguredPrusaLinkClient(printerConfig, settings.PrusaLinkTimeout, settings.PrusaLinkFileDownloadTimeout)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	diagnostics, err := client.GetDiagnostics()
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, diagnostics)
}

func (ws *WebServer) getJobReconciliationHandler(c *gin.Context) {
	checkpoint, err := ws.bridge.loadPrinterJobCheckpoint(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if checkpoint == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "printer has no job checkpoint"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"job_name":          checkpoint.JobName,
		"source_path":       checkpoint.SourcePath,
		"last_state":        checkpoint.LastState,
		"progress":          checkpoint.Progress,
		"accounting_status": checkpoint.AccountingStatus,
		"terminal_state":    checkpoint.TerminalState,
		"started_at":        checkpoint.StartedAt,
	})
}

func (ws *WebServer) resolveJobReconciliationHandler(c *gin.Context) {
	var request struct {
		Outcome string `json:"outcome" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "outcome is required"})
		return
	}
	if err := ws.bridge.resolvePrinterJobCheckpoint(c.Param("id"), request.Outcome); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"resolved": true, "outcome": strings.ToUpper(strings.TrimSpace(request.Outcome))})
}

// addPrinterHandler adds a new printer configuration
func (ws *WebServer) addPrinterHandler(c *gin.Context) {
	// Serialize printer operations to prevent race conditions
	ws.operationMutex.Lock()
	defer ws.operationMutex.Unlock()

	var printerConfig PrinterConfig
	if err := c.ShouldBindJSON(&printerConfig); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	printerConfig, err := ApplyPrinterPreset(printerConfig)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate printer configuration
	if err := validatePrinterConfig(printerConfig); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate address
	if err := validateAddress(printerConfig.IPAddress); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Generate a unique printer ID using nanosecond timestamp + random component
	printerID := fmt.Sprintf("printer_%d_%d", time.Now().UnixNano(), time.Now().Nanosecond()%1000)

	// Save the printer configuration
	if err := ws.bridge.SavePrinterConfig(printerID, printerConfig); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Reload configuration to include the new printer
	if err := ws.reloadBridgeConfig(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload configuration"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Printer added successfully", "printer_id": printerID})
}

// updatePrinterHandler updates an existing printer configuration
func (ws *WebServer) updatePrinterHandler(c *gin.Context) {
	// Serialize printer operations to prevent race conditions
	ws.operationMutex.Lock()
	defer ws.operationMutex.Unlock()

	printerID := c.Param("id")

	var request struct {
		PrinterConfig
		ClearAPIKey               bool `json:"clear_api_key"`
		ClearPrusaLinkCredentials bool `json:"clear_prusalink_credentials"`
		ClearPrusaLinkPassword    bool `json:"clear_prusalink_password"`
		ClearPrusaLinkCustomCAPEM bool `json:"clear_prusalink_custom_ca_pem"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	printerConfig := request.PrinterConfig
	existingConfig, exists := ws.printerConfigByID(printerID)
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "Printer not found"})
		return
	}
	originChanged := strings.TrimRight(strings.TrimSpace(printerConfig.IPAddress), "/") != strings.TrimRight(strings.TrimSpace(existingConfig.IPAddress), "/")
	if originChanged {
		if existingConfig.APIKey != "" && printerConfig.APIKey == "" && !request.ClearAPIKey {
			c.JSON(http.StatusBadRequest, gin.H{"error": "changing the PrusaLink address requires re-entering or explicitly clearing the API key"})
			return
		}
		if (existingConfig.PrusaLinkUsername != "" || existingConfig.PrusaLinkPassword != "") && printerConfig.PrusaLinkPassword == "" && !request.ClearPrusaLinkCredentials {
			c.JSON(http.StatusBadRequest, gin.H{"error": "changing the PrusaLink address requires re-entering or explicitly clearing Digest credentials"})
			return
		}
		if existingConfig.PrusaLinkCustomCAPEM != "" && printerConfig.PrusaLinkCustomCAPEM == "" && !request.ClearPrusaLinkCustomCAPEM {
			c.JSON(http.StatusBadRequest, gin.H{"error": "changing the PrusaLink address requires re-entering or explicitly clearing the custom CA"})
			return
		}
	}
	// Connection secrets are write-only. Empty values retain the stored value;
	// clients replace a secret by sending a non-empty value.
	if printerConfig.APIKey == "" && !request.ClearAPIKey {
		printerConfig.APIKey = existingConfig.APIKey
	}
	if request.ClearPrusaLinkCredentials {
		printerConfig.PrusaLinkUsername = ""
		printerConfig.PrusaLinkPassword = ""
	} else {
		if printerConfig.PrusaLinkUsername == "" {
			printerConfig.PrusaLinkUsername = existingConfig.PrusaLinkUsername
		}
		if printerConfig.PrusaLinkPassword == "" && !request.ClearPrusaLinkPassword {
			printerConfig.PrusaLinkPassword = existingConfig.PrusaLinkPassword
		}
	}
	if printerConfig.PrusaLinkCustomCAPEM == "" && !request.ClearPrusaLinkCustomCAPEM {
		printerConfig.PrusaLinkCustomCAPEM = existingConfig.PrusaLinkCustomCAPEM
	}
	printerConfig, err := ApplyPrinterPreset(printerConfig)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate printer configuration
	if err := validatePrinterConfig(printerConfig); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate address
	if err := validateAddress(printerConfig.IPAddress); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Auto-detect model if address or API key changed, or if model is currently "Unknown"
	if printerConfig.Model == "" || printerConfig.Model == ModelUnknown {
		log.Printf("🔍 [Auto-Detection] Detecting model for printer %s (IP: %s)", printerID, printerConfig.IPAddress)

		// Create PrusaLink client for detection
		client, clientErr := newConfiguredPrusaLinkClient(printerConfig, 10, 60)
		if clientErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": clientErr.Error()})
			return
		}

		// Try to get printer info
		printerInfo, err := client.GetPrinterInfo()
		if err != nil {
			log.Printf("⚠️ [Auto-Detection] Failed to detect model for %s: %v (keeping current model: %s)",
				printerConfig.IPAddress, err, printerConfig.Model)
		} else {
			// Use shared model detection function
			detectedModel := detectPrinterModel(printerInfo.Hostname)

			if detectedModel != ModelUnknown {
				log.Printf("✅ [Auto-Detection] Detected model for %s: '%s' -> %s",
					printerConfig.IPAddress, printerInfo.Hostname, detectedModel)
				printerConfig.Model = detectedModel
			} else {
				log.Printf("❌ [Auto-Detection] No pattern matched for hostname '%s' from %s",
					printerInfo.Hostname, printerConfig.IPAddress)
			}
		}
	}

	// Save the updated printer configuration
	if err := ws.bridge.SavePrinterConfig(printerID, printerConfig); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Reload configuration to include the updated printer
	if err := ws.reloadBridgeConfig(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload configuration"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Printer updated successfully"})
}

// deletePrinterHandler deletes a printer configuration
func (ws *WebServer) deletePrinterHandler(c *gin.Context) {
	// Serialize printer operations to prevent race conditions
	ws.operationMutex.Lock()
	defer ws.operationMutex.Unlock()

	printerID := c.Param("id")

	// Delete the printer configuration
	if err := ws.bridge.DeletePrinterConfig(printerID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Reload configuration to remove the deleted printer
	if err := ws.reloadBridgeConfig(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload configuration"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Printer deleted successfully"})
}

// getToolheadNamesHandler returns all toolhead names for a printer
func (ws *WebServer) getToolheadNamesHandler(c *gin.Context) {
	printerID := c.Param("id")

	// Verify printer exists
	printerConfigs, err := ws.bridge.GetAllPrinterConfigs()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	printerConfig, exists := printerConfigs[printerID]
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "Printer not found"})
		return
	}

	// Get all toolhead names
	toolheadNames, err := ws.bridge.GetAllToolheadNames(printerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Build response with all toolheads (including defaults for unnamed ones)
	result := make(map[int]string)
	for toolheadID := 0; toolheadID < printerConfig.Toolheads; toolheadID++ {
		if name, exists := toolheadNames[toolheadID]; exists {
			result[toolheadID] = name
		} else {
			result[toolheadID] = fmt.Sprintf("Toolhead %d", toolheadID)
		}
	}

	c.JSON(http.StatusOK, gin.H{"toolhead_names": result})
}

// updateToolheadNameHandler updates a toolhead's display name
func (ws *WebServer) updateToolheadNameHandler(c *gin.Context) {
	printerID := c.Param("id")
	toolheadIDStr := c.Param("toolhead_id")

	// Parse toolhead ID
	toolheadID, err := strconv.Atoi(toolheadIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid toolhead ID"})
		return
	}

	// Verify printer exists
	printerConfigs, err := ws.bridge.GetAllPrinterConfigs()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	printerConfig, exists := printerConfigs[printerID]
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "Printer not found"})
		return
	}

	// Validate toolhead ID is within range
	if toolheadID < 0 || toolheadID >= printerConfig.Toolheads {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Toolhead ID must be between 0 and %d", printerConfig.Toolheads-1)})
		return
	}

	// Parse request body
	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON or missing 'name' field"})
		return
	}

	// Update toolhead name
	if err := ws.bridge.SetToolheadName(printerID, toolheadID, req.Name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Toolhead name updated successfully"})
}

// detectPrinterModel detects printer model from hostname
func detectPrinterModel(hostname string) string {
	model := DetectPrinterModel(hostname)
	log.Printf("🎯 [Detection] Final result: hostname='%s' -> model='%s'", hostname, model)
	return model
}

// detectPrinterHandler detects printer model from PrusaLink API
func (ws *WebServer) detectPrinterHandler(c *gin.Context) {
	var req struct {
		IPAddress            string `json:"ip_address" binding:"required"`
		APIKey               string `json:"api_key"`
		PrusaLinkUsername    string `json:"prusalink_username"`
		PrusaLinkPassword    string `json:"prusalink_password"`
		PrusaLinkCustomCAPEM string `json:"prusalink_custom_ca_pem"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON"})
		return
	}

	// Validate address
	if err := validateAddress(req.IPAddress); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	log.Printf("🔍 [Detection] Starting printer model detection for IP: %s", req.IPAddress)

	// Create PrusaLink client
	client, err := newConfiguredPrusaLinkClient(PrinterConfig{
		IPAddress:            req.IPAddress,
		APIKey:               req.APIKey,
		PrusaLinkUsername:    req.PrusaLinkUsername,
		PrusaLinkPassword:    req.PrusaLinkPassword,
		PrusaLinkCustomCAPEM: req.PrusaLinkCustomCAPEM,
	}, 10, 60)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Try to get printer info, but don't fail if it times out
	printerInfo, err := client.GetPrinterInfo()
	if err != nil {
		log.Printf("❌ [Detection] Failed to get printer info from %s: %v", req.IPAddress, err)
		// If API call fails, return default values instead of error
		// This allows users to add printers even if they're offline
		c.JSON(http.StatusOK, gin.H{
			"model":    ModelUnknown,
			"hostname": "Unknown",
			"detected": false,
			"warning":  "Could not connect to printer. You can still add it manually.",
		})
		return
	}

	log.Printf("📥 [Detection] Received printer info: hostname='%s'", printerInfo.Hostname)

	// Use shared model detection function
	model := detectPrinterModel(printerInfo.Hostname)

	// Return detected information (toolheads will be provided by user)
	c.JSON(http.StatusOK, gin.H{
		"model":    model,
		"hostname": printerInfo.Hostname,
		"detected": true,
	})
}

// testSpoolmanConnectionHandler tests the connection to Spoolman
func (ws *WebServer) testSpoolmanConnectionHandler(c *gin.Context) {
	if err := ws.bridge.spoolmanSnapshot().TestConnection(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "connected": false})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Connection successful", "connected": true})
}

func (ws *WebServer) spoolmanCapabilitiesHandler(c *gin.Context) {
	capabilities, err := ws.bridge.spoolmanSnapshot().DetectCapabilities()
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, capabilities)
}

func (ws *WebServer) lookupSpoolmanTagHandler(c *gin.Context) {
	uid := strings.TrimSpace(c.Param("uid"))
	if uid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tag UID is required"})
		return
	}
	spool, err := ws.bridge.spoolmanSnapshot().LookupSpoolByTagUID(uid)
	if errors.Is(err, ErrSpoolmanTagAPIUnsupported) {
		c.JSON(http.StatusNotImplemented, gin.H{"error": err.Error(), "supported": false})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"supported": true, "spool": spool})
}

func (ws *WebServer) associateSpoolmanTagHandler(c *gin.Context) {
	spoolID, err := strconv.Atoi(c.Param("id"))
	if err != nil || spoolID < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid spool ID is required"})
		return
	}
	var request struct {
		UID    string `json:"uid" binding:"required"`
		Format string `json:"format"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.UID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tag UID is required"})
		return
	}
	if request.Format == "" {
		request.Format = "openprinttag"
	}
	tag, err := ws.bridge.spoolmanSnapshot().AssociateTagWithSpool(spoolID, request.UID, request.Format)
	if errors.Is(err, ErrSpoolmanTagAPIUnsupported) {
		c.JSON(http.StatusNotImplemented, gin.H{"error": err.Error(), "supported": false})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"supported": true, "tag": tag})
}

func (ws *WebServer) getSpoolConsumptionAuthorityHandler(c *gin.Context) {
	spoolID, err := strconv.Atoi(c.Param("id"))
	if err != nil || spoolID < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid spool ID is required"})
		return
	}
	authority, err := ws.bridge.GetSpoolConsumptionAuthority(spoolID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"spool_id": spoolID, "authority": authority})
}

func (ws *WebServer) updateSpoolConsumptionAuthorityHandler(c *gin.Context) {
	spoolID, err := strconv.Atoi(c.Param("id"))
	if err != nil || spoolID < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid spool ID is required"})
		return
	}
	var request struct {
		Authority ConsumptionAuthority `json:"authority" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "authority is required"})
		return
	}
	if err := ws.bridge.SetSpoolConsumptionAuthority(spoolID, request.Authority); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"spool_id": spoolID, "authority": request.Authority})
}

// debugSpoolmanHandler provides detailed debug information about Spoolman data
func (ws *WebServer) debugSpoolmanHandler(c *gin.Context) {
	spools, err := ws.bridge.spoolmanSnapshot().GetAllSpools()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	debugInfo := gin.H{
		"spool_count": len(spools),
		"spools":      spools,
		"raw_data":    make([]gin.H, len(spools)),
	}

	// Add raw field analysis
	for i, spool := range spools {
		debugInfo["raw_data"].([]gin.H)[i] = gin.H{
			"id":               spool.ID,
			"name":             spool.Name,
			"brand":            spool.Brand,
			"material":         spool.Material,
			"color":            spool.Filament.ColorHex,
			"remaining_length": spool.RemainingLength,
			"name_empty":       spool.Name == "",
			"brand_empty":      spool.Brand == "",
			"material_empty":   spool.Material == "",
			"color_empty":      spool.Filament.ColorHex == "",
		}
	}

	c.JSON(http.StatusOK, debugInfo)
}

// getPrintErrorsHandler returns all unacknowledged print errors
func (ws *WebServer) getPrintErrorsHandler(c *gin.Context) {
	errors := ws.bridge.GetPrintErrors()
	c.JSON(http.StatusOK, gin.H{
		"errors": errors,
	})
}

// acknowledgePrintErrorHandler acknowledges a print error
func (ws *WebServer) acknowledgePrintErrorHandler(c *gin.Context) {
	// Ensure we always return JSON
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Panic in acknowledgePrintErrorHandler: %v", r)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		}
	}()

	errorID := c.Param("id")
	if errorID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Error ID is required"})
		return
	}

	if err := ws.bridge.AcknowledgePrintError(errorID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Error acknowledged"})
}

// reloadBridgeConfig reloads the bridge configuration after changes
func (ws *WebServer) reloadBridgeConfig() error {
	// Reload configuration to include changes
	if err := ws.bridge.ReloadConfig(); err != nil {
		return fmt.Errorf("failed to reload configuration: %w", err)
	}
	return nil
}

// Start starts the web server
func (ws *WebServer) Start(port string) error {
	return ws.router.Run(":" + port)
}

type nfcAssignmentInput struct {
	spoolID           int
	spoolIDText       string
	locationText      string
	printerName       string
	toolheadID        int
	locationName      string
	isPrinterLocation bool
}

func (ws *WebServer) parseNFCAssignmentInput(spoolIDText string, locationText string) (nfcAssignmentInput, error) {
	input := nfcAssignmentInput{
		spoolIDText:  strings.TrimSpace(spoolIDText),
		locationText: strings.TrimSpace(locationText),
	}
	if input.spoolIDText == "" && input.locationText == "" {
		return input, fmt.Errorf("a spool or location tag value is required")
	}
	if input.spoolIDText != "" {
		spoolID, err := strconv.Atoi(input.spoolIDText)
		if err != nil || spoolID <= 0 {
			return input, fmt.Errorf("invalid spool ID")
		}
		input.spoolID = spoolID
	}
	if input.locationText != "" {
		printerName, toolheadID, locationName, isPrinterLocation, err := ws.bridge.parseLocationParam(input.locationText)
		if err != nil {
			return input, err
		}
		input.printerName = printerName
		input.toolheadID = toolheadID
		input.locationName = locationName
		input.isPrinterLocation = isPrinterLocation
	}
	return input, nil
}

// nfcAssignConfirmationHandler previews a scanned tag without changing state.
func (ws *WebServer) nfcAssignConfirmationHandler(c *gin.Context) {
	input, err := ws.parseNFCAssignmentInput(c.Query("spool"), c.Query("location"))
	if err != nil {
		c.HTML(http.StatusBadRequest, "nfc_error.html", gin.H{"Error": err.Error()})
		return
	}
	description := input.locationText
	if input.spoolID > 0 {
		description = fmt.Sprintf("Spool %d", input.spoolID)
		if input.locationText != "" {
			description += " and location " + input.locationText
		}
	}
	c.HTML(http.StatusOK, "nfc_confirm.html", gin.H{
		"Description": description,
		"SpoolID":     input.spoolID,
		"Location":    input.locationText,
	})
}

// nfcAssignHandler applies a confirmed NFC scan. Its POST route is protected
// by managementOriginMiddleware before this handler runs.
func (ws *WebServer) nfcAssignHandler(c *gin.Context) {
	spoolIDText := c.PostForm("spool")
	locationText := c.PostForm("location")
	// Authenticated non-browser clients may use POST query parameters.
	if spoolIDText == "" {
		spoolIDText = c.Query("spool")
	}
	if locationText == "" {
		locationText = c.Query("location")
	}
	input, err := ws.parseNFCAssignmentInput(spoolIDText, locationText)
	if err != nil {
		c.HTML(http.StatusBadRequest, "nfc_error.html", gin.H{"Error": err.Error()})
		return
	}
	clientIP := getClientIP(c.ClientIP())
	serviceSessionID := generateSessionID(clientIP)

	// Create or update session
	session, err := ws.bridge.createOrUpdateSession(serviceSessionID, input.spoolID, input.printerName, input.toolheadID, input.locationName, input.isPrinterLocation)
	if err != nil {
		c.HTML(http.StatusInternalServerError, "nfc_error.html", gin.H{
			"Error": "Failed to create session: " + err.Error(),
		})
		return
	}

	// Check if session is complete
	if session.isSessionComplete() {
		// Complete the assignment
		err = ws.bridge.AssignSpoolToLocation(session.SpoolID, session.PrinterName, session.ToolheadID, session.LocationName, session.IsPrinterLocation)
		if err != nil {
			c.HTML(http.StatusInternalServerError, "nfc_error.html", gin.H{
				"Error": "Assignment failed: " + err.Error(),
			})
			return
		}

		// Broadcast update to all connected clients
		ws.BroadcastStatus()

		// Clean up session
		ws.bridge.deleteSession(serviceSessionID)

		// Show success page
		c.HTML(http.StatusOK, "nfc_success.html", gin.H{
			"SpoolID":           session.SpoolID,
			"PrinterName":       session.PrinterName,
			"ToolheadID":        session.ToolheadID,
			"IsPrinterLocation": session.IsPrinterLocation,
			"LocationName":      session.LocationName,
		})
		return
	}

	// Session not complete, show progress
	var message string
	if session.HasSpool && !session.HasLocation {
		message = fmt.Sprintf("Spool %d selected. Now scan a location tag.", session.SpoolID)
	} else if session.HasLocation && !session.HasSpool {
		if session.IsPrinterLocation {
			message = fmt.Sprintf("Location %s - Toolhead %d selected. Now scan a spool tag.", session.PrinterName, session.ToolheadID)
		} else {
			message = fmt.Sprintf("Location '%s' selected. Now scan a spool tag.", session.LocationName)
		}
	} else {
		message = "Session started. Scan a spool or location tag."
	}

	c.HTML(http.StatusOK, "nfc_progress.html", gin.H{
		"Message":     message,
		"SessionID":   serviceSessionID,
		"HasSpool":    session.HasSpool,
		"HasLocation": session.HasLocation,
	})
}

// nfcUrlsHandler returns all available NFC URLs with QR codes
func (ws *WebServer) nfcUrlsHandler(c *gin.Context) {
	var urls []gin.H
	runtime := ws.bridge.runtimeSnapshot()
	publicOrigin, err := publicOriginForRequest(c.Request)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Get all spools
	spools, err := runtime.spoolman.GetAllSpools()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Generate spool URLs
	for _, spool := range spools {
		url := fmt.Sprintf("%s/api/nfc/assign?spool=%d", publicOrigin, spool.ID)

		// Safely get color hex
		colorHex := ""
		if spool.Filament != nil && spool.Filament.ColorHex != "" {
			colorHex = spool.Filament.ColorHex
			// Ensure it starts with #
			if !strings.HasPrefix(colorHex, "#") {
				colorHex = "#" + colorHex
			}
		}

		// Generate QR code
		qrCode, err := qrcode.Encode(url, qrcode.Medium, 256)
		if err != nil {
			log.Printf("Error generating QR code for spool %d: %v", spool.ID, err)
			// Continue without QR code if generation fails
			urls = append(urls, gin.H{
				"type":             "spool",
				"spool_id":         spool.ID,
				"spool_name":       spool.Name,
				"material":         spool.Material,
				"brand":            spool.Brand,
				"color_hex":        colorHex,
				"remaining_weight": spool.RemainingWeight,
				"url":              url,
				"qr_code_base64":   "",
			})
			continue
		}

		qrCodeBase64 := base64.StdEncoding.EncodeToString(qrCode)
		urls = append(urls, gin.H{
			"type":             "spool",
			"spool_id":         spool.ID,
			"spool_name":       spool.Name,
			"material":         spool.Material,
			"brand":            spool.Brand,
			"color_hex":        colorHex,
			"remaining_weight": spool.RemainingWeight,
			"url":              url,
			"qr_code_base64":   qrCodeBase64,
		})
	}

	// Get all filaments
	filaments, err := runtime.spoolman.GetAllFilaments()
	if err != nil {
		log.Printf("Warning: Failed to get filaments for NFC URLs: %v", err)
		filaments = []SpoolmanFilament{}
	}

	// Generate filament URLs
	for _, filament := range filaments {
		url := fmt.Sprintf("%s/filament/show/%d", runtime.config.SpoolmanURL, filament.ID)

		// Safely get color hex
		colorHex := ""
		if filament.ColorHex != "" {
			colorHex = filament.ColorHex
			// Ensure it starts with #
			if !strings.HasPrefix(colorHex, "#") {
				colorHex = "#" + colorHex
			}
		}

		// Get brand name
		brand := "Unknown Brand"
		if filament.Vendor != nil {
			brand = filament.Vendor.Name
		}

		// Generate QR code
		qrCode, err := qrcode.Encode(url, qrcode.Medium, 256)
		if err != nil {
			log.Printf("Error generating QR code for filament %d: %v", filament.ID, err)
			// Continue without QR code if generation fails
			urls = append(urls, gin.H{
				"type":           "filament",
				"filament_id":    filament.ID,
				"filament_name":  filament.Name,
				"material":       filament.Material,
				"brand":          brand,
				"color_hex":      colorHex,
				"extruder_temp":  filament.SettingsExtruderTemp,
				"bed_temp":       filament.SettingsBedTemp,
				"diameter":       filament.Diameter,
				"density":        filament.Density,
				"url":            url,
				"qr_code_base64": "",
			})
			continue
		}

		qrCodeBase64 := base64.StdEncoding.EncodeToString(qrCode)
		urls = append(urls, gin.H{
			"type":           "filament",
			"filament_id":    filament.ID,
			"filament_name":  filament.Name,
			"material":       filament.Material,
			"brand":          brand,
			"color_hex":      colorHex,
			"extruder_temp":  filament.SettingsExtruderTemp,
			"bed_temp":       filament.SettingsBedTemp,
			"diameter":       filament.Diameter,
			"density":        filament.Density,
			"url":            url,
			"qr_code_base64": qrCodeBase64,
		})
	}

	// Get Spoolman locations
	spoolmanLocations, err := runtime.spoolman.GetLocations()
	if err != nil {
		log.Printf("Warning: Failed to get Spoolman locations: %v", err)
		spoolmanLocations = []SpoolmanLocation{}
	}

	// Get printer configurations to build a map of printer toolhead location names
	printerConfigs, err := ws.bridge.GetAllPrinterConfigs()
	if err != nil {
		log.Printf("Warning: Failed to get printer configurations: %v", err)
		printerConfigs = make(map[string]PrinterConfig)
	}

	printerLocationNames := make(map[string]bool)
	for printerID, printerConfig := range printerConfigs {
		toolheadNames, err := ws.bridge.GetAllToolheadNames(printerID)
		if err != nil {
			toolheadNames = make(map[int]string)
		}
		for toolheadID := 0; toolheadID < printerConfig.Toolheads; toolheadID++ {
			var displayName string
			if name, exists := toolheadNames[toolheadID]; exists {
				displayName = name
			} else {
				displayName = fmt.Sprintf("Toolhead %d", toolheadID)
			}
			locationName := fmt.Sprintf("%s - %s", printerConfig.Name, displayName)
			printerLocationNames[locationName] = true
		}
	}

	// Generate location URLs for Spoolman locations only (no virtual printer toolhead locations)
	for _, location := range spoolmanLocations {
		// Skip archived locations
		if location.Archived {
			continue
		}

		// Skip locations with empty or whitespace-only names
		if strings.TrimSpace(location.Name) == "" {
			continue
		}

		locationParam := location.Name
		nfcUrl := fmt.Sprintf("%s/api/nfc/assign?location=%s", publicOrigin, neturl.QueryEscape(locationParam))

		// Generate QR code
		qrCode, err := qrcode.Encode(nfcUrl, qrcode.Medium, 256)
		if err != nil {
			log.Printf("Error generating QR code for Spoolman location %s: %v", locationParam, err)
			// Continue without QR code if generation fails
			urls = append(urls, gin.H{
				"type":           "location",
				"location_type":  "storage",
				"location_name":  location.Name,
				"display_name":   location.Name,
				"url":            nfcUrl,
				"qr_code_base64": "",
				"is_local_only":  false, // All Spoolman locations are synced
			})
			continue
		}

		qrCodeBase64 := base64.StdEncoding.EncodeToString(qrCode)
		urls = append(urls, gin.H{
			"type":           "location",
			"location_type":  "storage",
			"location_name":  location.Name,
			"display_name":   location.Name,
			"url":            nfcUrl,
			"qr_code_base64": qrCodeBase64,
			"is_local_only":  false, // All Spoolman locations are synced
		})
	}

	// Sort URLs: filaments first, then spools, then locations alphabetically by display name
	sort.Slice(urls, func(i, j int) bool {
		typeI := urls[i]["type"].(string)
		typeJ := urls[j]["type"].(string)

		// Filaments come first, then spools, then locations
		if typeI != typeJ {
			if typeI == "filament" {
				return true
			}
			if typeJ == "filament" {
				return false
			}
			if typeI == "spool" {
				return true
			}
			if typeJ == "spool" {
				return false
			}
			// Both are locations
			return true
		}

		// Both are the same type - apply appropriate sorting
		if typeI == "location" {
			// Locations: sort by display name (case-insensitive)
			displayNameI := urls[i]["display_name"].(string)
			displayNameJ := urls[j]["display_name"].(string)
			return strings.ToLower(displayNameI) < strings.ToLower(displayNameJ)
		}

		if typeI == "filament" {
			// Filaments: sort by ID (same as GetAllFilaments)
			idI := urls[i]["filament_id"].(int)
			idJ := urls[j]["filament_id"].(int)
			return idI < idJ
		}

		if typeI == "spool" {
			// Spools: sort by display name (Material - Brand - Name), then by remaining weight
			// This matches the sorting logic in GetAllSpools()
			materialI := urls[i]["material"].(string)
			materialJ := urls[j]["material"].(string)
			brandI := urls[i]["brand"].(string)
			brandJ := urls[j]["brand"].(string)
			nameI := urls[i]["spool_name"].(string)
			nameJ := urls[j]["spool_name"].(string)

			// Create display names for comparison (same as getSpoolDisplayName())
			displayNameI := fmt.Sprintf("%s - %s - %s", materialI, brandI, nameI)
			displayNameJ := fmt.Sprintf("%s - %s - %s", materialJ, brandJ, nameJ)

			if displayNameI != displayNameJ {
				return displayNameI < displayNameJ
			}

			// If display names are the same, sort by remaining weight (ascending - use less filament first)
			weightI := urls[i]["remaining_weight"].(float64)
			weightJ := urls[j]["remaining_weight"].(float64)
			return weightI < weightJ
		}

		return false
	})

	// Get Spoolman URL for the response
	spoolmanURL := runtime.spoolman.GetBaseURL()

	c.JSON(http.StatusOK, gin.H{
		"urls":         urls,
		"spoolman_url": spoolmanURL,
	})
}

// nfcSessionStatusHandler returns the current session status
func (ws *WebServer) nfcSessionStatusHandler(c *gin.Context) {
	clientIP := getClientIP(c.ClientIP())
	sessionID := generateSessionID(clientIP)

	session, err := ws.bridge.getSession(sessionID)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"active": false,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"active":              true,
		"session_id":          session.SessionID,
		"has_spool":           session.HasSpool,
		"has_location":        session.HasLocation,
		"spool_id":            session.SpoolID,
		"printer_name":        session.PrinterName,
		"toolhead_id":         session.ToolheadID,
		"location_name":       session.LocationName,
		"is_printer_location": session.IsPrinterLocation,
		"expires_at":          session.ExpiresAt,
	})
}

// Location Management Handlers

// getLocationsHandler returns only Spoolman locations (no virtual printer toolheads)
func (ws *WebServer) getLocationsHandler(c *gin.Context) {
	spoolman := ws.bridge.spoolmanSnapshot()
	// Get Spoolman locations
	spoolmanLocations, err := spoolman.GetLocations()
	if err != nil {
		log.Printf("Warning: Failed to get Spoolman locations: %v", err)
		spoolmanLocations = []SpoolmanLocation{}
	}

	// Only return Spoolman locations (no virtual printer toolhead locations)
	var allLocations []gin.H
	for _, loc := range spoolmanLocations {
		// Skip archived locations
		if loc.Archived {
			continue
		}

		// Skip locations with empty or whitespace-only names
		if strings.TrimSpace(loc.Name) == "" {
			continue
		}

		allLocations = append(allLocations, gin.H{
			"name":       loc.Name,
			"type":       "storage",
			"is_virtual": false,
		})
	}

	// Get Spoolman URL for the message
	spoolmanURL := spoolman.GetBaseURL()

	c.JSON(http.StatusOK, gin.H{
		"locations":    allLocations,
		"spoolman_url": spoolmanURL,
	})
}

// getLocationStatusHandler returns detailed status information for a specific location
func (ws *WebServer) getLocationStatusHandler(c *gin.Context) {
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Location name is required"})
		return
	}

	// Check if location exists in Spoolman
	location, err := ws.bridge.spoolmanSnapshot().FindLocationByName(name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if location == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Location not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"name":     location.Name,
		"id":       location.ID,
		"comment":  location.Comment,
		"archived": location.Archived,
	})
}

// createLocationHandler creates a new location in Spoolman
func (ws *WebServer) createLocationHandler(c *gin.Context) {
	var req struct {
		Name        string `json:"name" binding:"required"`
		Type        string `json:"type"`
		PrinterName string `json:"printer_name,omitempty"`
		ToolheadID  int    `json:"toolhead_id,omitempty"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("createLocationHandler: bad request: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	log.Printf("createLocationHandler: creating location name='%s' in Spoolman", req.Name)
	location, err := ws.bridge.spoolmanSnapshot().GetOrCreateLocation(req.Name)
	if err != nil {
		log.Printf("createLocationHandler: failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"name":     location.Name,
		"id":       location.ID,
		"comment":  location.Comment,
		"archived": location.Archived,
	})
}

// updateLocationHandler updates a location in Spoolman
func (ws *WebServer) updateLocationHandler(c *gin.Context) {
	spoolman := ws.bridge.spoolmanSnapshot()
	oldName := c.Param("name")
	if oldName == "" {
		log.Printf("updateLocationHandler: missing location name")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Location name is required"})
		return
	}

	var req struct {
		Name string `json:"name" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("updateLocationHandler: bad request for name='%s': %v", oldName, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	log.Printf("updateLocationHandler: renaming '%s' to '%s' in Spoolman", oldName, req.Name)
	if err := spoolman.UpdateLocationByName(oldName, req.Name); err != nil {
		log.Printf("updateLocationHandler: failed for name='%s': %v", oldName, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Get updated location
	location, err := spoolman.FindLocationByName(req.Name)
	if err != nil {
		log.Printf("Warning: Could not get updated location '%s': %v", req.Name, err)
		c.JSON(http.StatusOK, gin.H{"message": "Location updated successfully"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Location updated successfully",
		"location": gin.H{
			"name":     location.Name,
			"id":       location.ID,
			"comment":  location.Comment,
			"archived": location.Archived,
		},
	})
}

// deleteLocationHandler archives a location in Spoolman (locations are archived, not deleted)
func (ws *WebServer) deleteLocationHandler(c *gin.Context) {
	spoolman := ws.bridge.spoolmanSnapshot()
	name := c.Param("name")
	if name == "" {
		log.Printf("deleteLocationHandler: missing location name")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Location name is required"})
		return
	}

	// Find location by name
	location, err := spoolman.FindLocationByName(name)
	if err != nil {
		log.Printf("deleteLocationHandler: error finding location '%s': %v", name, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if location == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Location not found"})
		return
	}

	// Archive the location (Spoolman doesn't support deletion, only archiving)
	log.Printf("deleteLocationHandler: archiving location '%s' (ID: %d)", name, location.ID)
	if err := spoolman.ArchiveLocation(location.ID); err != nil {
		log.Printf("deleteLocationHandler: failed to archive location '%s': %v", name, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to archive location"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Location archived successfully"})
}
