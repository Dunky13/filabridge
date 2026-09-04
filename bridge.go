package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// FilamentBridge manages the connection between PrusaLink and Spoolman
type FilamentBridge struct {
	config            *Config
	spoolman          *SpoolmanClient
	db                *sql.DB
	processingPrints  map[string]bool // Track prints being processed
	printerJobLocks   map[string]*sync.Mutex
	printerJobLocksMu sync.Mutex
	instanceID        string
	diagnosticsLogged map[string]bool
	printErrors       map[string]PrintError // Store print processing errors
	errorMutex        sync.RWMutex
	mutex             sync.RWMutex
	observations      *printerObservationSource
	configChanged     chan struct{}
}

// bridgeRuntimeSnapshot binds a configuration snapshot to the Spoolman client
// created from that same configuration publication.
type bridgeRuntimeSnapshot struct {
	config   *Config
	spoolman *SpoolmanClient
}

func cloneConfig(config *Config) *Config {
	if config == nil {
		return nil
	}

	clone := *config
	clone.Printers = make(map[string]PrinterConfig, len(config.Printers))
	for id, printer := range config.Printers {
		clone.Printers[id] = printer
	}
	return &clone
}

func (b *FilamentBridge) runtimeSnapshot() bridgeRuntimeSnapshot {
	b.mutex.RLock()
	defer b.mutex.RUnlock()
	return bridgeRuntimeSnapshot{
		config:   cloneConfig(b.config),
		spoolman: b.spoolman,
	}
}

func (b *FilamentBridge) spoolmanSnapshot() *SpoolmanClient {
	return b.runtimeSnapshot().spoolman
}

func newBridgeInstanceID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err == nil {
		return hex.EncodeToString(bytes)
	}
	return fmt.Sprintf("instance-%d", time.Now().UnixNano())
}

// ToolheadMapping represents a mapping between a printer toolhead and a spool
type ToolheadMapping struct {
	PrinterID   string    `json:"printer_id,omitempty"`
	PrinterName string    `json:"printer_name"`
	ToolheadID  int       `json:"toolhead_id"`
	SpoolID     int       `json:"spool_id"`
	MappedAt    time.Time `json:"mapped_at"`
	DisplayName string    `json:"display_name,omitempty"` // Custom toolhead name or empty for default
}

// PrintHistory represents a record of filament usage
type PrintHistory struct {
	ID            int       `json:"id"`
	PrinterID     string    `json:"printer_id,omitempty"`
	PrinterName   string    `json:"printer_name"`
	ToolheadID    int       `json:"toolhead_id"`
	ToolheadName  string    `json:"toolhead_name,omitempty"`
	SpoolID       *int      `json:"spool_id"`
	FilamentUsed  float64   `json:"filament_used"`
	PrintStarted  time.Time `json:"print_started"`
	PrintFinished time.Time `json:"print_finished"`
	JobName       string    `json:"job_name"`
	SourcePath    string    `json:"source_path,omitempty"`
	PrintState    string    `json:"print_state,omitempty"`
}

// PrintError represents a failed print processing attempt
type PrintError struct {
	ID           string    `json:"id"`
	PrinterName  string    `json:"printer_name"`
	Filename     string    `json:"filename"`
	Error        string    `json:"error"`
	Timestamp    time.Time `json:"timestamp"`
	Acknowledged bool      `json:"acknowledged"`
}

// PrinterStatus represents the current status of all printers
type PrinterStatus struct {
	Printers         map[string]PrinterData             `json:"printers"`
	ToolheadMappings map[string]map[int]ToolheadMapping `json:"toolhead_mappings"`
	Timestamp        time.Time                          `json:"timestamp"`
}

// PrinterData represents data for a single printer
type PrinterData struct {
	Name          string  `json:"name"`
	State         string  `json:"state"`
	CurrentJob    string  `json:"current_job,omitempty"`
	Progress      float64 `json:"progress"`
	PrintTime     int     `json:"print_time"`
	PrintTimeLeft int     `json:"print_time_left"`
}

// NewFilamentBridge creates a new FilamentBridge instance
func NewFilamentBridge(config *Config) (*FilamentBridge, error) {
	config = cloneConfig(config)
	spoolman := NewSpoolmanClient(DefaultSpoolmanURL, SpoolmanTimeout, "", "")
	if config != nil && config.SpoolmanURL != "" {
		spoolman = NewSpoolmanClient(config.SpoolmanURL, config.SpoolmanTimeout, config.SpoolmanUsername, config.SpoolmanPassword)
	}
	bridge := &FilamentBridge{
		config:            config,
		spoolman:          spoolman,
		processingPrints:  make(map[string]bool),
		printerJobLocks:   make(map[string]*sync.Mutex),
		diagnosticsLogged: make(map[string]bool),
		instanceID:        newBridgeInstanceID(),
		printErrors:       make(map[string]PrintError),
		observations:      newPrinterObservationSource(),
		configChanged:     make(chan struct{}, 1),
	}

	// Initialize database
	if err := bridge.initDatabase(); err != nil {
		if bridge.db != nil {
			_ = bridge.db.Close()
		}
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	return bridge, nil
}

// initDatabase initializes the SQLite database
func (b *FilamentBridge) initDatabase() error {
	dbFile := DefaultDBFileName
	if config := b.GetConfigSnapshot(); config != nil && config.DBFile != "" {
		dbFile = config.DBFile
	}
	// Check for environment variable (path only, append filename)
	if envDBPath := os.Getenv("FILABRIDGE_DB_PATH"); envDBPath != "" {
		dbFile = filepath.Join(envDBPath, DefaultDBFileName)
	}

	db, err := sql.Open("sqlite3", sqliteDSN(dbFile))
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	b.db = db

	if err := runSchemaMigrations(b.db); err != nil {
		return fmt.Errorf("failed to run schema migrations: %w", err)
	}

	// Initialize default configuration
	if err := b.initializeDefaultConfig(); err != nil {
		return fmt.Errorf("failed to initialize default configuration: %w", err)
	}

	// Migrate existing FilaBridge locations to Spoolman
	if err := b.migrateLocationsToSpoolman(); err != nil {
		log.Printf("Warning: Failed to migrate locations to Spoolman: %v", err)
		// Don't fail initialization if migration fails
	}

	// Create Spoolman locations for existing toolhead mappings
	if err := b.migrateToolheadMappingsToSpoolman(); err != nil {
		log.Printf("Warning: Failed to migrate toolhead mappings to Spoolman: %v", err)
		// Don't fail initialization if migration fails
	}

	return nil
}

// migrateLocationsToSpoolman migrates existing FilaBridge locations to Spoolman
func (b *FilamentBridge) migrateLocationsToSpoolman() error {
	spoolman := b.spoolmanSnapshot()
	// Check if fb_locations table exists by trying to query it
	rows, err := b.db.Query("SELECT name, type, printer_name, toolhead_id FROM fb_locations")
	if err != nil {
		// Table doesn't exist or is empty, nothing to migrate
		return nil
	}
	defer rows.Close()

	migratedCount := 0
	for rows.Next() {
		var name, locationType, printerName sql.NullString
		var toolheadID sql.NullInt64

		if err := rows.Scan(&name, &locationType, &printerName, &toolheadID); err != nil {
			log.Printf("Warning: Failed to scan location row during migration: %v", err)
			continue
		}

		if !name.Valid || name.String == "" {
			continue
		}

		locationName := name.String

		// Skip if this is a virtual printer toolhead location (will be created on-demand)
		if b.isVirtualPrinterToolheadLocation(locationName) {
			log.Printf("Migration: Skipping virtual printer toolhead location '%s'", locationName)
			continue
		}

		// Check if location exists in Spoolman
		// Note: Spoolman API doesn't support creating locations via POST.
		// Locations must be created manually in Spoolman UI or are auto-created when referenced in spools.
		existingLocation, err := spoolman.FindLocationByName(locationName)
		if err != nil {
			log.Printf("Warning: Failed to check if location '%s' exists in Spoolman: %v", locationName, err)
			continue
		}

		if existingLocation == nil {
			log.Printf("Migration: Location '%s' does not exist in Spoolman. It will be created when referenced in a spool, or can be created manually in Spoolman UI.", locationName)
		} else {
			migratedCount++
			log.Printf("Migration: Location '%s' already exists in Spoolman", locationName)
		}
	}

	if migratedCount > 0 {
		log.Printf("Migration: Successfully migrated %d location(s) from FilaBridge to Spoolman", migratedCount)
	}

	return nil
}

// migrateToolheadMappingsToSpoolman creates Spoolman locations for existing toolhead mappings
func (b *FilamentBridge) migrateToolheadMappingsToSpoolman() error {
	spoolman := b.spoolmanSnapshot()
	// Get all printer configs
	printerConfigs, err := b.GetAllPrinterConfigs()
	if err != nil {
		return fmt.Errorf("failed to get printer configs: %w", err)
	}

	// Get all toolhead mappings
	allMappings, err := b.GetAllToolheadMappings()
	if err != nil {
		return fmt.Errorf("failed to get toolhead mappings: %w", err)
	}

	createdCount := 0
	for printerID, printerMappings := range allMappings {
		printerConfig, exists := printerConfigs[printerID]
		if !exists {
			log.Printf("Migration: Could not find printer config for printer ID '%s', skipping", printerID)
			continue
		}
		printerName := printerConfig.Name

		// Get toolhead names for this printer
		toolheadNames, err := b.GetAllToolheadNames(printerID)
		if err != nil {
			log.Printf("Warning: Failed to get toolhead names for printer %s: %v", printerID, err)
			toolheadNames = make(map[int]string)
		}

		// Create locations for each toolhead mapping
		for toolheadID := range printerMappings {
			// Get display name (custom or default)
			var displayName string
			if name, exists := toolheadNames[toolheadID]; exists {
				displayName = name
			} else {
				displayName = fmt.Sprintf("Toolhead %d", toolheadID)
			}

			locationName := fmt.Sprintf("%s - %s", printerName, displayName)

			// Check if location exists in Spoolman
			// Note: Spoolman API doesn't support creating locations via POST.
			// Locations will be auto-created when spools are assigned to toolheads.
			existingLocation, err := spoolman.FindLocationByName(locationName)
			if err != nil {
				log.Printf("Warning: Failed to check if toolhead location '%s' exists in Spoolman: %v", locationName, err)
				continue
			}

			if existingLocation == nil {
				log.Printf("Migration: Toolhead location '%s' does not exist in Spoolman. It will be created when a spool is assigned to this toolhead.", locationName)
			} else {
				createdCount++
				log.Printf("Migration: Toolhead location '%s' already exists in Spoolman", locationName)
			}
		}
	}

	if createdCount > 0 {
		log.Printf("Migration: Successfully created %d toolhead location(s) in Spoolman", createdCount)
	}

	return nil
}

// initializeDefaultConfig sets up default configuration values
func (b *FilamentBridge) initializeDefaultConfig() error {
	defaultConfigs := map[string]string{
		ConfigKeyPrinterIPs:                      "", // Comma-separated list of printer IP addresses
		ConfigKeyAPIKey:                          "", // PrusaLink API key for authentication
		ConfigKeySpoolmanURL:                     DefaultSpoolmanURL,
		ConfigKeySpoolmanUsername:                "", // Spoolman basic auth username (optional)
		ConfigKeySpoolmanPassword:                "", // Spoolman basic auth password (optional)
		ConfigKeyConsumptionAuthority:            string(ConsumptionAuthoritySpoolmanLed),
		ConfigKeyPollInterval:                    fmt.Sprintf("%d", DefaultPollInterval),
		ConfigKeyWebPort:                         DefaultWebPort,
		ConfigKeyPrusaLinkTimeout:                fmt.Sprintf("%d", PrusaLinkTimeout),
		ConfigKeyPrusaLinkFileDownloadTimeout:    fmt.Sprintf("%d", PrusaLinkFileDownloadTimeout),
		ConfigKeySpoolmanTimeout:                 fmt.Sprintf("%d", SpoolmanTimeout),
		ConfigKeyAutoAssignPreviousSpoolEnabled:  "false", // Enable auto-assignment of previous spool to default location
		ConfigKeyAutoAssignPreviousSpoolLocation: "",      // Default location name for auto-assigned previous spools
	}

	// INSERT OR IGNORE doubles as a forward migration when new defaults are added.
	for key, value := range defaultConfigs {
		_, err := b.db.Exec(
			"INSERT OR IGNORE INTO configuration (key, value, description) VALUES (?, ?, ?)",
			key, value, getConfigDescription(key),
		)
		if err != nil {
			return fmt.Errorf("failed to insert default config %s: %w", key, err)
		}
	}

	return nil
}

// getConfigDescription returns a description for a configuration key
func getConfigDescription(key string) string {
	descriptions := map[string]string{
		ConfigKeyPrinterIPs:                      "Comma-separated list of printer IP addresses for PrusaLink",
		ConfigKeyAPIKey:                          "PrusaLink API key for authentication",
		ConfigKeySpoolmanURL:                     "URL of Spoolman instance",
		ConfigKeySpoolmanUsername:                "Spoolman basic auth username (optional, leave empty if not using basic auth)",
		ConfigKeySpoolmanPassword:                "Spoolman basic auth password (optional, leave empty if not using basic auth)",
		ConfigKeyConsumptionAuthority:            "Sole source allowed to author filament consumption: spoolman-led, tag-led, or observed-only",
		ConfigKeyPollInterval:                    "Polling interval in seconds",
		ConfigKeyWebPort:                         "Port for web interface",
		ConfigKeyPrusaLinkTimeout:                "PrusaLink API timeout in seconds",
		ConfigKeyPrusaLinkFileDownloadTimeout:    "PrusaLink file download timeout in seconds",
		ConfigKeySpoolmanTimeout:                 "Spoolman API timeout in seconds",
		ConfigKeyAutoAssignPreviousSpoolEnabled:  "Enable automatic assignment of previous spool to default location when assigning new spool to toolhead",
		ConfigKeyAutoAssignPreviousSpoolLocation: "Default location name where previous spools will be automatically assigned (must exist as a location)",
	}
	if desc, exists := descriptions[key]; exists {
		return desc
	}
	return "Configuration value"
}

// GetConfigValue gets a configuration value from the database
func (b *FilamentBridge) GetConfigValue(key string) (string, error) {
	var value string
	err := b.db.QueryRow("SELECT value FROM configuration WHERE key = ?", key).Scan(&value)
	if err != nil {
		return "", fmt.Errorf("failed to get config value for %s: %w", key, err)
	}
	return value, nil
}

// SetConfigValue sets a configuration value in the database
func (b *FilamentBridge) SetConfigValue(key, value string) error {
	_, err := b.db.Exec(
		"INSERT OR REPLACE INTO configuration (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)",
		key, value,
	)
	if err != nil {
		return fmt.Errorf("failed to set config value for %s: %w", key, err)
	}
	return nil
}

// GetAllConfig gets all configuration values
func (b *FilamentBridge) GetAllConfig() (map[string]string, error) {
	rows, err := b.db.Query("SELECT key, value FROM configuration")
	if err != nil {
		return nil, fmt.Errorf("failed to get all config: %w", err)
	}
	defer rows.Close()

	config := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("failed to scan config row: %w", err)
		}
		config[key] = value
	}

	return config, nil
}

// GetAutoAssignPreviousSpoolEnabled gets whether auto-assignment of previous spool is enabled
func (b *FilamentBridge) GetAutoAssignPreviousSpoolEnabled() (bool, error) {
	value, err := b.GetConfigValue(ConfigKeyAutoAssignPreviousSpoolEnabled)
	if err != nil {
		// If key doesn't exist, return false (default)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return value == "true", nil
}

// SetAutoAssignPreviousSpoolEnabled sets whether auto-assignment of previous spool is enabled
func (b *FilamentBridge) SetAutoAssignPreviousSpoolEnabled(enabled bool) error {
	value := "false"
	if enabled {
		value = "true"
	}
	return b.SetConfigValue(ConfigKeyAutoAssignPreviousSpoolEnabled, value)
}

// GetAutoAssignPreviousSpoolLocation gets the default location name for auto-assigned previous spools
func (b *FilamentBridge) GetAutoAssignPreviousSpoolLocation() (string, error) {
	value, err := b.GetConfigValue(ConfigKeyAutoAssignPreviousSpoolLocation)
	if err != nil {
		// If key doesn't exist, return empty string (default)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return value, nil
}

// SetAutoAssignPreviousSpoolLocation sets the default location name for auto-assigned previous spools
func (b *FilamentBridge) SetAutoAssignPreviousSpoolLocation(location string) error {
	return b.SetConfigValue(ConfigKeyAutoAssignPreviousSpoolLocation, location)
}

func (b *FilamentBridge) getCurrentToolheadSpoolID(printerName string, toolheadID int) (int, error) {
	identity, err := b.resolvePrinterReference(printerName)
	if err != nil {
		return 0, err
	}
	var spoolID int
	err = b.db.QueryRow(
		"SELECT spool_id FROM toolhead_mappings WHERE printer_id = ? AND toolhead_id = ?",
		identity.ID, toolheadID,
	).Scan(&spoolID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to get current spool mapping: %w", err)
	}

	return spoolID, nil
}

func (b *FilamentBridge) resolvePreviousSpoolLocation(explicitLocation string) (string, error) {
	spoolman := b.spoolmanSnapshot()
	locationName := strings.TrimSpace(explicitLocation)
	if locationName != "" {
		location, err := spoolman.FindLocationByName(locationName)
		if err != nil {
			return "", fmt.Errorf("failed to validate previous spool location '%s': %w", locationName, err)
		}
		if location == nil {
			return "", fmt.Errorf("previous spool location '%s' does not exist", locationName)
		}
		return locationName, nil
	}

	enabled, err := b.GetAutoAssignPreviousSpoolEnabled()
	if err != nil {
		return "", fmt.Errorf("failed to check auto-assign previous spool setting: %w", err)
	}
	if !enabled {
		return "", nil
	}

	locationName, err = b.GetAutoAssignPreviousSpoolLocation()
	if err != nil {
		return "", fmt.Errorf("failed to get auto-assign previous spool location setting: %w", err)
	}
	locationName = strings.TrimSpace(locationName)
	if locationName == "" {
		return "", nil
	}

	location, err := spoolman.FindLocationByName(locationName)
	if err != nil {
		return "", fmt.Errorf("failed to validate auto-assign previous spool location '%s': %w", locationName, err)
	}
	if location == nil {
		return "", fmt.Errorf("auto-assign previous spool location '%s' does not exist", locationName)
	}

	return locationName, nil
}

func (b *FilamentBridge) getToolheadLocationName(printerName string, toolheadID int) string {
	identity, err := b.resolvePrinterReference(printerName)
	if err != nil {
		return fmt.Sprintf("%s - Toolhead %d", printerName, toolheadID)
	}

	displayName := fmt.Sprintf("Toolhead %d", toolheadID)
	name, err := b.GetToolheadName(identity.ID, toolheadID)
	if err == nil {
		displayName = name
	}

	return fmt.Sprintf("%s - %s", identity.Name, displayName)
}

func (b *FilamentBridge) updateSpoolToolheadLocation(spoolID int, printerName string, toolheadID int) error {
	spoolman := b.spoolmanSnapshot()
	locationName := b.getToolheadLocationName(printerName, toolheadID)
	if _, err := spoolman.GetOrCreateLocation(locationName); err != nil {
		log.Printf("Warning: Failed to create/verify location '%s' in Spoolman: %v", locationName, err)
	}
	if err := spoolman.UpdateSpoolLocation(spoolID, locationName); err != nil {
		return fmt.Errorf("failed to update spool %d to toolhead location '%s': %w", spoolID, locationName, err)
	}
	return nil
}

func (b *FilamentBridge) setToolheadMappingRecord(printerName string, toolheadID int, spoolID int) error {
	identity, err := b.resolvePrinterReference(printerName)
	if err != nil {
		return err
	}
	b.mutex.Lock()
	defer b.mutex.Unlock()

	rows, err := b.db.Query(
		`SELECT p.name, m.toolhead_id FROM toolhead_mappings m
		 JOIN printer_configs p ON p.printer_id = m.printer_id
		 WHERE m.spool_id = ? AND NOT (m.printer_id = ? AND m.toolhead_id = ?)`,
		spoolID, identity.ID, toolheadID,
	)
	if err != nil {
		return fmt.Errorf("failed to check existing spool assignments: %w", err)
	}
	defer rows.Close()

	if rows.Next() {
		var existingPrinterName string
		var existingToolheadID int
		if err := rows.Scan(&existingPrinterName, &existingToolheadID); err != nil {
			return fmt.Errorf("failed to scan existing assignment: %w", err)
		}
		return fmt.Errorf("spool %d is already assigned to %s toolhead %d", spoolID, existingPrinterName, existingToolheadID)
	}

	_, err = b.db.Exec(
		`INSERT INTO toolhead_mappings (printer_id, toolhead_id, spool_id, mapped_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(printer_id, toolhead_id) DO UPDATE SET spool_id = excluded.spool_id, mapped_at = excluded.mapped_at`,
		identity.ID, toolheadID, spoolID, time.Now(),
	)
	if err != nil {
		return fmt.Errorf("failed to set toolhead mapping: %w", err)
	}

	log.Printf("Mapped %s toolhead %d to spool %d", printerName, toolheadID, spoolID)
	return nil
}

// SwitchToolheadSpool updates a dashboard toolhead mapping and relocates replaced spools.
func (b *FilamentBridge) SwitchToolheadSpool(printerName string, toolheadID int, spoolID int, previousSpoolLocation string) error {
	previousSpoolID, err := b.getCurrentToolheadSpoolID(printerName, toolheadID)
	if err != nil {
		return err
	}

	var resolvedPreviousLocation string
	if previousSpoolID > 0 && previousSpoolID != spoolID {
		resolvedPreviousLocation, err = b.resolvePreviousSpoolLocation(previousSpoolLocation)
		if err != nil {
			return err
		}
		if resolvedPreviousLocation == "" {
			return fmt.Errorf("previous spool %d needs a storage location or configured default location", previousSpoolID)
		}
	}

	if spoolID == 0 {
		if err := b.UnmapToolhead(printerName, toolheadID); err != nil {
			return err
		}
	} else {
		if err := b.setToolheadMappingRecord(printerName, toolheadID, spoolID); err != nil {
			return err
		}
		if err := b.updateSpoolToolheadLocation(spoolID, printerName, toolheadID); err != nil {
			return err
		}
	}

	if resolvedPreviousLocation != "" {
		if err := b.AssignSpoolToLocation(previousSpoolID, "", 0, resolvedPreviousLocation, false); err != nil {
			return fmt.Errorf("failed to assign previous spool %d to location '%s': %w", previousSpoolID, resolvedPreviousLocation, err)
		}
	}

	return nil
}

// GetAllPrinterConfigs gets all printer configurations
func (b *FilamentBridge) GetAllPrinterConfigs() (map[string]PrinterConfig, error) {
	rows, err := b.db.Query("SELECT printer_id, name, model, ip_address, api_key, prusalink_username, prusalink_password, prusalink_custom_ca_pem, toolheads FROM printer_configs")
	if err != nil {
		return nil, fmt.Errorf("failed to get printer configs: %w", err)
	}
	defer rows.Close()

	configs := make(map[string]PrinterConfig)
	for rows.Next() {
		var printerID, name, model, ipAddress, apiKey, username, password, customCAPEM string
		var toolheads int
		if err := rows.Scan(&printerID, &name, &model, &ipAddress, &apiKey, &username, &password, &customCAPEM, &toolheads); err != nil {
			return nil, fmt.Errorf("failed to scan printer config row: %w", err)
		}
		configs[printerID] = PrinterConfig{
			Name:                 name,
			Model:                model,
			IPAddress:            ipAddress,
			APIKey:               apiKey,
			PrusaLinkUsername:    username,
			PrusaLinkPassword:    password,
			PrusaLinkCustomCAPEM: customCAPEM,
			Toolheads:            toolheads,
		}
	}

	return configs, nil
}

// SavePrinterConfig saves a printer configuration
func (b *FilamentBridge) SavePrinterConfig(printerID string, config PrinterConfig) error {
	b.mutex.Lock()
	var previousName string
	err := b.db.QueryRow("SELECT name FROM printer_configs WHERE printer_id = ?", printerID).Scan(&previousName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		b.mutex.Unlock()
		return fmt.Errorf("failed to inspect existing printer config: %w", err)
	}

	_, err = b.db.Exec(`
		INSERT INTO printer_configs (
			printer_id, name, model, ip_address, api_key, prusalink_username,
			prusalink_password, prusalink_custom_ca_pem, toolheads
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(printer_id) DO UPDATE SET
			name = excluded.name,
			model = excluded.model,
			ip_address = excluded.ip_address,
			api_key = excluded.api_key,
			prusalink_username = excluded.prusalink_username,
			prusalink_password = excluded.prusalink_password,
			prusalink_custom_ca_pem = excluded.prusalink_custom_ca_pem,
			toolheads = excluded.toolheads,
			updated_at = CURRENT_TIMESTAMP
	`, printerID, config.Name, config.Model, config.IPAddress, config.APIKey,
		config.PrusaLinkUsername, config.PrusaLinkPassword, config.PrusaLinkCustomCAPEM, config.Toolheads)
	if err != nil {
		b.mutex.Unlock()
		return fmt.Errorf("failed to save printer config: %w", err)
	}
	b.mutex.Unlock()

	if previousName != "" && previousName != config.Name {
		for toolheadID := 0; toolheadID < config.Toolheads; toolheadID++ {
			locationName := b.getToolheadLocationName(printerID, toolheadID)
			if _, err := b.db.Exec(`UPDATE nfc_sessions SET location_name = ?
				WHERE printer_id = ? AND toolhead_id = ? AND is_printer_location = 1`, locationName, printerID, toolheadID); err != nil {
				log.Printf("Warning: Failed to update NFC session location after renaming printer %s: %v", printerID, err)
			}
		}
		mappings, err := b.GetToolheadMappings(printerID)
		if err != nil {
			log.Printf("Warning: Failed to load toolhead mappings after renaming printer %s: %v", printerID, err)
			return nil
		}
		for toolheadID, mapping := range mappings {
			if err := b.updateSpoolToolheadLocation(mapping.SpoolID, printerID, toolheadID); err != nil {
				log.Printf("Warning: Failed to update spool location after renaming printer %s: %v", printerID, err)
			}
		}
	}
	return nil
}

// DeletePrinterConfig deletes a printer configuration
func (b *FilamentBridge) DeletePrinterConfig(printerID string) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	tx, err := b.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin printer deletion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec("DELETE FROM logical_tool_routes WHERE printer_id = ?", printerID); err != nil {
		return fmt.Errorf("failed to delete logical tool routes: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM printer_job_checkpoints WHERE printer_id = ?", printerID); err != nil {
		return fmt.Errorf("failed to delete printer job checkpoint: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM printer_job_adjustments WHERE printer_id = ?", printerID); err != nil {
		return fmt.Errorf("failed to delete printer job adjustments: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM toolhead_names WHERE printer_id = ?", printerID); err != nil {
		return fmt.Errorf("failed to delete toolhead names: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM printer_configs WHERE printer_id = ?", printerID); err != nil {
		return fmt.Errorf("failed to delete printer config: %w", err)
	}
	return tx.Commit()
}

// GetToolheadName gets the display name for a toolhead, or returns default "Toolhead {ID}"
func (b *FilamentBridge) GetToolheadName(printerID string, toolheadID int) (string, error) {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	var displayName string
	err := b.db.QueryRow(
		"SELECT display_name FROM toolhead_names WHERE printer_id = ? AND toolhead_id = ?",
		printerID, toolheadID,
	).Scan(&displayName)

	if err == sql.ErrNoRows {
		// Return default name if not found
		return fmt.Sprintf("Toolhead %d", toolheadID), nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get toolhead name: %w", err)
	}

	return displayName, nil
}

// SetToolheadName sets the display name for a toolhead
func (b *FilamentBridge) SetToolheadName(printerID string, toolheadID int, name string) error {
	// Validate name is not empty
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("toolhead name cannot be empty")
	}

	// Get printer config to find printer name (before acquiring lock)
	printerConfigs, err := b.GetAllPrinterConfigs()
	if err != nil {
		return fmt.Errorf("failed to get printer configs: %w", err)
	}

	printerConfig, exists := printerConfigs[printerID]
	if !exists {
		return fmt.Errorf("printer %s not found", printerID)
	}

	printerName := printerConfig.Name

	// Get old toolhead name to calculate old location name (before acquiring lock)
	var oldDisplayName string
	oldName, err := b.GetToolheadName(printerID, toolheadID)
	if err == nil {
		oldDisplayName = oldName
	} else {
		oldDisplayName = fmt.Sprintf("Toolhead %d", toolheadID)
	}

	oldLocationName := fmt.Sprintf("%s - %s", printerName, oldDisplayName)
	newLocationName := fmt.Sprintf("%s - %s", printerName, name)

	// Update toolhead name in database
	b.mutex.Lock()
	_, err = b.db.Exec(
		"INSERT OR REPLACE INTO toolhead_names (printer_id, toolhead_id, display_name) VALUES (?, ?, ?)",
		printerID, toolheadID, name,
	)
	b.mutex.Unlock()

	if err != nil {
		return fmt.Errorf("failed to set toolhead name: %w", err)
	}

	// If location name changed, update Spoolman (outside of lock)
	if oldLocationName != newLocationName {
		spoolman := b.spoolmanSnapshot()
		// Get all spools from Spoolman
		spools, err := spoolman.GetAllSpools()
		if err != nil {
			log.Printf("Warning: Failed to get spools from Spoolman to update location names: %v", err)
		} else {
			// Find spools with the old location name and update them
			updatedCount := 0
			for _, spool := range spools {
				if spool.Location == oldLocationName {
					if err := spoolman.UpdateSpoolLocation(spool.ID, newLocationName); err != nil {
						log.Printf("Warning: Failed to update spool %d location from '%s' to '%s': %v", spool.ID, oldLocationName, newLocationName, err)
					} else {
						updatedCount++
					}
				}
			}

			// Ensure the new location exists in Spoolman
			if _, err := spoolman.GetOrCreateLocation(newLocationName); err != nil {
				log.Printf("Warning: Failed to create/verify location '%s' in Spoolman: %v", newLocationName, err)
			}

			if updatedCount > 0 {
				log.Printf("Updated %d spool(s) location from '%s' to '%s'", updatedCount, oldLocationName, newLocationName)
			}
		}
	}

	log.Printf("Set toolhead name for printer %s, toolhead %d: %s", printerID, toolheadID, name)
	return nil
}

// GetAllToolheadNames gets all toolhead display names for a printer
func (b *FilamentBridge) GetAllToolheadNames(printerID string) (map[int]string, error) {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	rows, err := b.db.Query(
		"SELECT toolhead_id, display_name FROM toolhead_names WHERE printer_id = ? ORDER BY toolhead_id",
		printerID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get toolhead names: %w", err)
	}
	defer rows.Close()

	names := make(map[int]string)
	for rows.Next() {
		var toolheadID int
		var displayName string
		if err := rows.Scan(&toolheadID, &displayName); err != nil {
			return nil, fmt.Errorf("failed to scan toolhead name row: %w", err)
		}
		names[toolheadID] = displayName
	}

	return names, nil
}

// GetConfigSnapshot returns a snapshot of the current config for safe iteration
func (b *FilamentBridge) GetConfigSnapshot() *Config {
	return b.runtimeSnapshot().config
}

// ReloadConfig reloads the configuration from the database
func (b *FilamentBridge) ReloadConfig() error {
	// Load config outside the lock to minimize lock time
	config, err := LoadConfig(b)
	if err != nil {
		return fmt.Errorf("failed to reload config: %w", err)
	}
	config = cloneConfig(config)
	spoolman := NewSpoolmanClient(config.SpoolmanURL, config.SpoolmanTimeout, config.SpoolmanUsername, config.SpoolmanPassword)

	// Publish matching config/client pointers together. Client construction and
	// all network I/O stay outside the bridge lock.
	b.mutex.Lock()
	b.config = config
	b.diagnosticsLogged = make(map[string]bool)
	b.spoolman = spoolman
	b.mutex.Unlock()
	b.signalConfigChanged()

	return nil
}

// IsFirstRun checks if this is the first time the application is running
func (b *FilamentBridge) IsFirstRun() (bool, error) {
	var count int
	err := b.db.QueryRow("SELECT COUNT(*) FROM printer_configs").Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check first run status: %w", err)
	}

	// If no printers are configured, this is a first run
	return count == 0, nil
}

// UpdateConfig updates the bridge configuration
func (b *FilamentBridge) UpdateConfig(config *Config) error {
	if config == nil {
		return fmt.Errorf("config is required")
	}
	config = cloneConfig(config)
	spoolman := NewSpoolmanClient(config.SpoolmanURL, config.SpoolmanTimeout, config.SpoolmanUsername, config.SpoolmanPassword)
	b.mutex.Lock()
	hadConfig := b.config != nil
	b.config = config
	b.diagnosticsLogged = make(map[string]bool)
	b.spoolman = spoolman
	b.mutex.Unlock()
	if hadConfig {
		b.signalConfigChanged()
	}

	return nil
}

func (b *FilamentBridge) signalConfigChanged() {
	select {
	case b.configChanged <- struct{}{}:
	default:
	}
}

func (b *FilamentBridge) configChanges() <-chan struct{} {
	return b.configChanged
}

// GetToolheadMapping gets spool ID mapped to a specific toolhead
func (b *FilamentBridge) GetToolheadMapping(printerName string, toolheadID int) (int, error) {
	identity, err := b.resolvePrinterReference(printerName)
	if err != nil {
		return 0, err
	}
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	var spoolID int
	err = b.db.QueryRow(
		"SELECT spool_id FROM toolhead_mappings WHERE printer_id = ? AND toolhead_id = ?",
		identity.ID, toolheadID,
	).Scan(&spoolID)

	if err == sql.ErrNoRows {
		return 0, nil // No mapping found
	}
	if err != nil {
		return 0, fmt.Errorf("failed to get toolhead mapping: %w", err)
	}

	return spoolID, nil
}

// SetToolheadMapping maps a spool to a specific toolhead
func (b *FilamentBridge) SetToolheadMapping(printerName string, toolheadID int, spoolID int) error {
	// Get the previous spool ID before replacing it (for auto-assignment feature)
	previousSpoolID, err := b.getCurrentToolheadSpoolID(printerName, toolheadID)
	if err != nil {
		return fmt.Errorf("failed to get previous spool mapping: %w", err)
	}

	if err := b.setToolheadMappingRecord(printerName, toolheadID, spoolID); err != nil {
		return err
	}

	// Check if auto-assign feature is enabled and we have a previous spool to assign
	enabled, err := b.GetAutoAssignPreviousSpoolEnabled()
	if err != nil {
		log.Printf("Warning: Failed to check auto-assign previous spool setting: %v", err)
		return nil // Don't fail the assignment if we can't check the setting
	}

	if enabled && previousSpoolID > 0 && previousSpoolID != spoolID {
		// Get the configured default location
		locationName, err := b.GetAutoAssignPreviousSpoolLocation()
		if err != nil {
			log.Printf("Warning: Failed to get auto-assign previous spool location setting: %v", err)
			return nil // Don't fail the assignment
		}

		if locationName != "" {
			spoolman := b.spoolmanSnapshot()
			// Verify the location exists in Spoolman
			location, err := spoolman.FindLocationByName(locationName)
			if err != nil || location == nil {
				log.Printf("Warning: Auto-assign previous spool location '%s' does not exist, skipping auto-assignment of spool %d", locationName, previousSpoolID)
				return nil // Don't fail the assignment
			}

			// Assign the previous spool to the default location
			// Use isPrinterLocation = false since this is a storage location
			if err := b.AssignSpoolToLocation(previousSpoolID, "", 0, locationName, false); err != nil {
				log.Printf("Warning: Failed to auto-assign previous spool %d to location '%s': %v", previousSpoolID, locationName, err)
				// Don't fail the original assignment if auto-assignment fails
			} else {
				log.Printf("Auto-assigned previous spool %d to location '%s'", previousSpoolID, locationName)
			}
		}
	}

	return nil
}

// GetToolheadMappings gets all toolhead mappings for a printer
func (b *FilamentBridge) GetToolheadMappings(printerName string) (map[int]ToolheadMapping, error) {
	identity, err := b.resolvePrinterReference(printerName)
	if err != nil {
		return nil, err
	}
	rows, err := b.db.Query(
		"SELECT toolhead_id, spool_id, mapped_at FROM toolhead_mappings WHERE printer_id = ?",
		identity.ID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	mappings := make(map[int]ToolheadMapping)
	for rows.Next() {
		var toolheadID, spoolID int
		var mappedAt time.Time
		if err := rows.Scan(&toolheadID, &spoolID, &mappedAt); err != nil {
			return nil, err
		}
		mappings[toolheadID] = ToolheadMapping{
			PrinterID:   identity.ID,
			PrinterName: identity.Name,
			ToolheadID:  toolheadID,
			SpoolID:     spoolID,
			MappedAt:    mappedAt,
		}
	}

	return mappings, nil
}

// GetAllToolheadMappings gets all toolhead mappings across all printers
func (b *FilamentBridge) GetAllToolheadMappings() (map[string]map[int]ToolheadMapping, error) {
	rows, err := b.db.Query(
		`SELECT m.printer_id, p.name, m.toolhead_id, m.spool_id, m.mapped_at
		 FROM toolhead_mappings m JOIN printer_configs p ON p.printer_id = m.printer_id
		 ORDER BY p.name, m.toolhead_id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	mappings := make(map[string]map[int]ToolheadMapping)
	for rows.Next() {
		var printerID, printerName string
		var toolheadID, spoolID int
		var mappedAt time.Time
		if err := rows.Scan(&printerID, &printerName, &toolheadID, &spoolID, &mappedAt); err != nil {
			return nil, err
		}

		if mappings[printerID] == nil {
			mappings[printerID] = make(map[int]ToolheadMapping)
		}

		mappings[printerID][toolheadID] = ToolheadMapping{
			PrinterID:   printerID,
			PrinterName: printerName,
			ToolheadID:  toolheadID,
			SpoolID:     spoolID,
			MappedAt:    mappedAt,
		}
	}

	return mappings, nil
}

// UnmapToolhead removes a spool mapping from a toolhead
func (b *FilamentBridge) UnmapToolhead(printerName string, toolheadID int) error {
	identity, err := b.resolvePrinterReference(printerName)
	if err != nil {
		return err
	}
	b.mutex.Lock()
	defer b.mutex.Unlock()

	_, err = b.db.Exec(
		"DELETE FROM toolhead_mappings WHERE printer_id = ? AND toolhead_id = ?",
		identity.ID, toolheadID,
	)
	if err != nil {
		return fmt.Errorf("failed to unmap toolhead: %w", err)
	}

	log.Printf("Unmapped %s toolhead %d", printerName, toolheadID)
	return nil
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}

	cloned := *value
	return &cloned
}

// LogPrintUsage logs filament usage for a print job
func (b *FilamentBridge) LogPrintUsage(printerName string, toolheadID int, spoolID *int, filamentUsed float64, jobName string) error {
	return b.LogPrintUsageWithSourcePath(printerName, toolheadID, spoolID, filamentUsed, jobName, "")
}

// LogPrintUsageWithSourcePath logs filament usage and retains the printer file path when known.
func (b *FilamentBridge) LogPrintUsageWithSourcePath(printerName string, toolheadID int, spoolID *int, filamentUsed float64, jobName string, sourcePath string) error {
	return b.logPrintUsageWithState(printerName, toolheadID, spoolID, filamentUsed, jobName, sourcePath, StateFinished)
}

func (b *FilamentBridge) logPrintUsageWithState(printerName string, toolheadID int, spoolID *int, filamentUsed float64, jobName string, sourcePath string, printState string) error {
	identity, err := b.resolvePrinterReference(printerName)
	if err != nil {
		return err
	}
	b.mutex.Lock()
	defer b.mutex.Unlock()

	// This compatibility path records an observation-time event. Monitored jobs use
	// the durable checkpoint path, which supplies the exact observed start time.
	observedAt := time.Now().UTC()

	_, err = b.db.Exec(
		"INSERT INTO print_history (printer_id, printer_name_at_event, toolhead_id, spool_id, filament_used, print_started, print_finished, job_name, source_path, print_state) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		identity.ID, identity.Name, toolheadID, spoolID, filamentUsed, observedAt, observedAt, jobName, strings.TrimSpace(sourcePath), printState,
	)
	if err != nil {
		return fmt.Errorf("failed to log print usage: %w", err)
	}

	return nil
}

func (b *FilamentBridge) getToolheadDisplayName(printerName string, toolheadID int) string {
	identity, err := b.resolvePrinterReference(printerName)
	if err != nil {
		return fmt.Sprintf("Toolhead %d", toolheadID)
	}
	name, err := b.GetToolheadName(identity.ID, toolheadID)
	if err == nil {
		return name
	}

	return fmt.Sprintf("Toolhead %d", toolheadID)
}

// GetPrintHistory returns latest print history entries.
func (b *FilamentBridge) GetPrintHistory(limit int) ([]PrintHistory, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := b.db.Query(`
		SELECT id, COALESCE(printer_id, ''), printer_name_at_event, toolhead_id, spool_id, filament_used, print_started, print_finished, job_name, source_path, COALESCE(print_state, '')
		FROM print_history
		ORDER BY print_finished DESC, id DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get print history: %w", err)
	}
	defer rows.Close()

	history := make([]PrintHistory, 0, limit)
	for rows.Next() {
		var entry PrintHistory
		var spoolID sql.NullInt64
		var sourcePath sql.NullString
		if err := rows.Scan(
			&entry.ID,
			&entry.PrinterID,
			&entry.PrinterName,
			&entry.ToolheadID,
			&spoolID,
			&entry.FilamentUsed,
			&entry.PrintStarted,
			&entry.PrintFinished,
			&entry.JobName,
			&sourcePath,
			&entry.PrintState,
		); err != nil {
			return nil, fmt.Errorf("failed to scan print history row: %w", err)
		}

		if spoolID.Valid {
			value := int(spoolID.Int64)
			entry.SpoolID = &value
		}
		if sourcePath.Valid {
			entry.SourcePath = sourcePath.String
		}

		printerReference := entry.PrinterID
		if printerReference == "" {
			printerReference = entry.PrinterName
		}
		entry.ToolheadName = b.getToolheadDisplayName(printerReference, entry.ToolheadID)
		history = append(history, entry)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate print history rows: %w", err)
	}

	return history, nil
}

func (b *FilamentBridge) getPrintHistoryByID(historyID int) (*PrintHistory, error) {
	var entry PrintHistory
	var spoolID sql.NullInt64
	var sourcePath sql.NullString
	err := b.db.QueryRow(`
		SELECT id, COALESCE(printer_id, ''), printer_name_at_event, toolhead_id, spool_id, filament_used, print_started, print_finished, job_name, source_path, COALESCE(print_state, '')
		FROM print_history
		WHERE id = ?
	`, historyID).Scan(
		&entry.ID,
		&entry.PrinterID,
		&entry.PrinterName,
		&entry.ToolheadID,
		&spoolID,
		&entry.FilamentUsed,
		&entry.PrintStarted,
		&entry.PrintFinished,
		&entry.JobName,
		&sourcePath,
		&entry.PrintState,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("print history entry %d not found", historyID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get print history entry %d: %w", historyID, err)
	}

	if spoolID.Valid {
		value := int(spoolID.Int64)
		entry.SpoolID = &value
	}
	if sourcePath.Valid {
		entry.SourcePath = sourcePath.String
	}

	printerReference := entry.PrinterID
	if printerReference == "" {
		printerReference = entry.PrinterName
	}
	entry.ToolheadName = b.getToolheadDisplayName(printerReference, entry.ToolheadID)
	return &entry, nil
}

func (b *FilamentBridge) setPrintHistorySourcePath(historyID int, sourcePath string) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	_, err := b.db.Exec("UPDATE print_history SET source_path = ? WHERE id = ?", strings.TrimSpace(sourcePath), historyID)
	if err != nil {
		return fmt.Errorf("failed to update print history source path for entry %d: %w", historyID, err)
	}

	return nil
}

func (b *FilamentBridge) getPrinterConfigByName(printerName string) (string, PrinterConfig, error) {
	identity, err := b.resolvePrinterReference(printerName)
	if err != nil {
		return "", PrinterConfig{}, err
	}
	printerConfigs, err := b.GetAllPrinterConfigs()
	if err != nil {
		return "", PrinterConfig{}, fmt.Errorf("failed to get printer configs: %w", err)
	}

	if printerConfig, exists := printerConfigs[identity.ID]; exists {
		return identity.ID, printerConfig, nil
	}

	return "", PrinterConfig{}, fmt.Errorf("printer %s not found", printerName)
}

func selectHistoryFilamentUsage(toolheadID int, filamentUsage map[int]float64) (float64, error) {
	if len(filamentUsage) == 0 {
		return 0, fmt.Errorf("no filament usage data returned from printer")
	}
	if weight, exists := filamentUsage[toolheadID]; exists && weight > 0 {
		return weight, nil
	}
	if len(filamentUsage) == 1 {
		for _, weight := range filamentUsage {
			if weight > 0 {
				return weight, nil
			}
		}
	}

	return 0, fmt.Errorf("printer returned filament usage for different toolheads: %+v", filamentUsage)
}

func (b *FilamentBridge) RefreshPrintHistoryFilamentUsage(historyID int, spoolID *int) (*PrintHistory, error) {
	entry, err := b.getPrintHistoryByID(historyID)
	if err != nil {
		return nil, err
	}

	printerReference := entry.PrinterID
	if printerReference == "" {
		printerReference = entry.PrinterName
	}
	_, printerConfig, err := b.getPrinterConfigByName(printerReference)
	if err != nil {
		return nil, err
	}

	settings := b.GetConfigSnapshot()
	if settings == nil {
		return nil, fmt.Errorf("config is unavailable")
	}
	prusaClient, err := newConfiguredPrusaLinkClient(printerConfig, settings.PrusaLinkTimeout, settings.PrusaLinkFileDownloadTimeout)
	if err != nil {
		return nil, err
	}

	sourcePath := strings.TrimSpace(entry.SourcePath)
	if sourcePath == "" {
		sourcePath, err = prusaClient.FindStoragePathForJobName(entry.JobName)
		if err != nil {
			return nil, err
		}
	}

	filamentUsage, err := prusaClient.GetFilamentUsageForFile(sourcePath)
	if err != nil {
		return nil, err
	}

	pulledWeight, err := selectHistoryFilamentUsage(entry.ToolheadID, filamentUsage)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(entry.SourcePath) != sourcePath {
		if err := b.setPrintHistorySourcePath(historyID, sourcePath); err != nil {
			return nil, err
		}
	}

	targetSpoolID := spoolID
	if targetSpoolID == nil {
		targetSpoolID = entry.SpoolID
	}

	if err := b.UpdatePrintHistory(historyID, targetSpoolID, pulledWeight); err != nil {
		return nil, err
	}

	updatedEntry, err := b.getPrintHistoryByID(historyID)
	if err != nil {
		return nil, err
	}
	updatedEntry.SourcePath = sourcePath

	return updatedEntry, nil
}

// UpdatePrintHistory corrects spool assignment and/or filament usage for an existing print.
func (b *FilamentBridge) UpdatePrintHistory(historyID int, spoolID *int, filamentUsed float64) error {
	if spoolID != nil && *spoolID <= 0 {
		return fmt.Errorf("spool_id must be greater than 0")
	}
	if filamentUsed < 0 {
		return fmt.Errorf("filament_used must be greater than or equal to 0")
	}

	entry, err := b.getPrintHistoryByID(historyID)
	if err != nil {
		return err
	}

	currentSpoolID := 0
	if entry.SpoolID != nil {
		currentSpoolID = *entry.SpoolID
	}

	nextSpoolID := 0
	if spoolID != nil {
		nextSpoolID = *spoolID
	}

	if currentSpoolID == nextSpoolID && math.Abs(entry.FilamentUsed-filamentUsed) < 0.0001 {
		return nil
	}
	spoolman := b.spoolmanSnapshot()

	if spoolID != nil {
		if _, err := spoolman.GetSpool(*spoolID); err != nil {
			return err
		}
	}

	if entry.SpoolID != nil && entry.FilamentUsed > 0 {
		if err := spoolman.AdjustSpoolUsage(*entry.SpoolID, -entry.FilamentUsed); err != nil {
			return fmt.Errorf("failed to revert usage from spool %d: %w", *entry.SpoolID, err)
		}
	}

	if spoolID != nil && filamentUsed > 0 {
		if err := spoolman.AdjustSpoolUsage(*spoolID, filamentUsed); err != nil {
			if entry.SpoolID != nil && entry.FilamentUsed > 0 {
				rollbackErr := spoolman.AdjustSpoolUsage(*entry.SpoolID, entry.FilamentUsed)
				if rollbackErr != nil {
					log.Printf("Failed to rollback print history correction for entry %d: %v", historyID, rollbackErr)
				}
			}
			return fmt.Errorf("failed to apply usage to spool %d: %w", *spoolID, err)
		}
	}

	b.mutex.Lock()
	defer b.mutex.Unlock()

	_, err = b.db.Exec("UPDATE print_history SET spool_id = ?, filament_used = ? WHERE id = ?", spoolID, filamentUsed, historyID)
	if err != nil {
		if spoolID != nil && filamentUsed > 0 {
			rollbackNewErr := spoolman.AdjustSpoolUsage(*spoolID, -filamentUsed)
			if rollbackNewErr != nil {
				log.Printf("Failed to rollback new spool usage for entry %d: %v", historyID, rollbackNewErr)
			}
		}
		if entry.SpoolID != nil && entry.FilamentUsed > 0 {
			rollbackOldErr := spoolman.AdjustSpoolUsage(*entry.SpoolID, entry.FilamentUsed)
			if rollbackOldErr != nil {
				log.Printf("Failed to restore original spool usage for entry %d: %v", historyID, rollbackOldErr)
			}
		}
		return fmt.Errorf("failed to update print history entry %d: %w", historyID, err)
	}

	return nil
}

// UpdatePrintHistorySpool corrects which spool was used for an existing print.
func (b *FilamentBridge) UpdatePrintHistorySpool(historyID int, spoolID *int) error {
	entry, err := b.getPrintHistoryByID(historyID)
	if err != nil {
		return err
	}

	return b.UpdatePrintHistory(historyID, spoolID, entry.FilamentUsed)
}

// MonitorPrinters monitors all printers for print status changes
func (b *FilamentBridge) MonitorPrinters() {
	b.pollPrinters(true)
}

// observePrinters refreshes status for web-only mode without reconciling jobs
// or changing inventory.
func (b *FilamentBridge) observePrinters() {
	b.pollPrinters(false)
}

func (b *FilamentBridge) pollPrinters(reconcile bool) {
	log.Printf("Monitoring printers at %s", time.Now().Format(time.RFC3339))

	// Get a safe snapshot of the config to prevent iteration issues
	configSnapshot := b.GetConfigSnapshot()
	if configSnapshot == nil || len(configSnapshot.Printers) == 0 {
		log.Printf("No printers configured - skipping monitoring")
		return
	}

	// Complete one coherent cycle before broadcasting its observations.
	var monitors sync.WaitGroup
	for printerID, printerConfig := range configSnapshot.Printers {
		if printerID == "no_printers" {
			continue // Skip placeholder
		}
		monitors.Add(1)
		go func(printerID string, config PrinterConfig) {
			defer monitors.Done()
			if err := b.pollPrusaLinkWithSettings(printerID, config, configSnapshot, reconcile); err != nil {
				log.Printf("Error monitoring printer %s (%s): %v", config.IPAddress, printerID, err)
			}
		}(printerID, printerConfig)
	}
	monitors.Wait()
}

// monitorPrusaLink monitors a single printer using PrusaLink API
func (b *FilamentBridge) monitorPrusaLink(printerID string, config PrinterConfig) error {
	return b.pollPrusaLinkWithSettings(printerID, config, b.GetConfigSnapshot(), true)
}

func (b *FilamentBridge) pollPrusaLinkWithSettings(printerID string, config PrinterConfig, settings *Config, reconcile bool) error {
	jobLock := b.printerJobLock(printerID)
	jobLock.Lock()
	defer jobLock.Unlock()

	log.Printf("Starting monitoring for printer %s (%s) at %s", printerID, config.IPAddress, config.Name)
	observation, err := b.pollPrusaLink(printerID, config, settings)
	if err != nil {
		b.observations.record(printerID, PrinterData{Name: config.Name, State: StateOffline})
		return err
	}
	b.observations.record(printerID, observation.data)
	if observation.status == nil || !reconcile {
		return nil
	}
	return b.reconcilePrusaLinkJob(printerID, config, observation.status, observation.job, observation.sourcePath, observation.jobDisplayName, observation.filamentUsage)
}

func (b *FilamentBridge) pollPrusaLink(printerID string, config PrinterConfig, settings *Config) (polledPrinterObservation, error) {
	offline := polledPrinterObservation{data: PrinterData{Name: config.Name, State: StateOffline}}
	timeout := PrusaLinkTimeout
	fileDownloadTimeout := PrusaLinkFileDownloadTimeout
	if settings != nil {
		timeout = settings.PrusaLinkTimeout
		fileDownloadTimeout = settings.PrusaLinkFileDownloadTimeout
	}
	client, err := newConfiguredPrusaLinkClient(config, timeout, fileDownloadTimeout)
	if err != nil {
		return offline, fmt.Errorf("invalid PrusaLink configuration: %w", err)
	}
	b.logPrusaLinkDiagnosticsOnce(printerID, client)

	status, err := client.GetStatus()
	if err != nil {
		log.Printf("Warning: Failed to get printer status from %s (%s): %v", config.IPAddress, printerID, err)
		return offline, nil // Don't fail the entire monitoring cycle for one printer
	}

	jobInfo, err := client.GetJobInfo()
	if err != nil {
		log.Printf("Warning: Failed to get job info from %s (%s): %v", config.IPAddress, printerID, err)
		// Continue with status-only monitoring if job info fails
		jobInfo = &PrusaLinkJob{}
	}

	currentState := status.Printer.State
	jobName := "No active job"
	currentJobFilename := ""
	currentJobDisplayName := ""
	if jobInfo.File.Name != "" || jobInfo.File.DisplayName != "" || jobInfo.File.Path != "" {
		currentJobFilename = joinPrusaStoragePath(jobInfo.File.Path, jobInfo.File.Name)
		currentJobDisplayName = resolvePrusaJobName(jobInfo.File.DisplayName, jobInfo.File.Name, currentJobFilename)
		jobName = currentJobDisplayName
	}

	currentJobUsage := cloneFilamentUsage(jobInfo.FilamentUsageByToolhead())
	data := PrinterData{
		Name:          config.Name,
		State:         status.Printer.State,
		Progress:      status.Job.Progress,
		PrintTime:     status.Job.TimePrinting,
		PrintTimeLeft: status.Job.TimeRemaining,
	}
	if isActivePrinterState(data.State) {
		data.CurrentJob = currentJobDisplayName
	}

	log.Printf("Printer %s (%s): state=%s, job=%s, file=%s",
		config.IPAddress, printerID, currentState, jobName, currentJobFilename)
	return polledPrinterObservation{
		data:           data,
		status:         status,
		job:            jobInfo,
		sourcePath:     currentJobFilename,
		jobDisplayName: currentJobDisplayName,
		filamentUsage:  currentJobUsage,
	}, nil
}

// handlePrusaLinkPrintFinished handles when a print job finishes via PrusaLink
func (b *FilamentBridge) handlePrusaLinkPrintFinished(config PrinterConfig, storagePath string, jobName string, filamentUsage map[int]float64) error {
	return b.handlePrusaLinkPrintFinishedWithMappings(config, storagePath, jobName, filamentUsage, nil, StateFinished)
}

func (b *FilamentBridge) handlePrusaLinkPrintFinishedWithMappings(config PrinterConfig, storagePath string, jobName string, filamentUsage map[int]float64, toolAssignments map[int]PrintToolAssignment, printState string) error {
	return b.handlePrusaLinkPrintFinishedWithCheckpoint(config, storagePath, jobName, filamentUsage, toolAssignments, printState, nil)
}

func (b *FilamentBridge) handlePrusaLinkPrintFinishedWithCheckpoint(config PrinterConfig, storagePath string, jobName string, filamentUsage map[int]float64, toolAssignments map[int]PrintToolAssignment, printState string, checkpoint *printerJobCheckpoint) error {
	if checkpoint == nil {
		identity, err := b.resolvePrinterReference(resolvePrinterName(config))
		if err != nil {
			return err
		}
		jobLock := b.printerJobLock(identity.ID)
		jobLock.Lock()
		defer jobLock.Unlock()
		return b.accountCompletedPrint(config, CompletedPrintObservation{
			PrinterID:    identity.ID,
			JobName:      jobName,
			SourcePath:   storagePath,
			FilamentUsed: filamentUsage,
			StartedAt:    time.Now().UTC(),
			PrintState:   printState,
		})
	}
	if jobName == "" {
		jobName = resolvePrusaJobName("", "", storagePath)
	}

	log.Printf("Print finished via PrusaLink (%s): %s (%s)", config.IPAddress, jobName, storagePath)

	printerName := resolvePrinterName(config)

	// Use storage path captured while print was active.
	if storagePath == "" {
		errorMsg := "no filename available for print processing"
		b.addPrintError(printerName, resolvePrusaJobName(jobName, "", ""), errorMsg)
		return fmt.Errorf("%s", errorMsg)
	}

	if len(filamentUsage) > 0 {
		log.Printf("Using filament usage from PrusaLink job metadata: %+v", filamentUsage)
	} else {
		log.Printf("Fetching print file metadata for filament usage: %s", storagePath)

		settings := b.GetConfigSnapshot()
		if settings == nil {
			return fmt.Errorf("config is unavailable")
		}
		prusaClient, err := newConfiguredPrusaLinkClient(config, settings.PrusaLinkTimeout, settings.PrusaLinkFileDownloadTimeout)
		if err != nil {
			return fmt.Errorf("invalid PrusaLink configuration: %w", err)
		}
		resolvedUsage, err := prusaClient.GetFilamentUsageForFile(storagePath)
		if err != nil {
			errorMsg := fmt.Sprintf("failed to extract filament usage from PrusaLink: %v", err)
			return b.logPrintHistoryWithoutUsage(checkpoint, config, storagePath, jobName, errorMsg)
		}

		filamentUsage = resolvedUsage
	}

	// Check if we got any filament usage data
	if len(filamentUsage) == 0 {
		errorMsg := "no filament usage data found in PrusaLink metadata"
		return b.logPrintHistoryWithoutUsage(checkpoint, config, storagePath, jobName, errorMsg)
	}

	log.Printf("Successfully collected filament usage: %+v", filamentUsage)

	// Process filament usage using helper function
	if err := b.processFilamentUsageWithCheckpoint(printerName, filamentUsage, jobName, storagePath, toolAssignments, printState, checkpoint); err != nil {
		log.Printf("Error processing filament usage: %v", err)
		return err
	}

	return nil
}

func (b *FilamentBridge) logPrintHistoryWithoutUsage(checkpoint *printerJobCheckpoint, config PrinterConfig, sourcePath string, jobName string, errorMsg string) error {
	b.addPrintError(checkpoint.PrinterName, jobName, errorMsg)

	toolheadID := 0
	var spoolID *int
	if assignment, exists := checkpoint.ToolAssignments[0]; exists {
		toolheadID = assignment.PhysicalToolheadID
		if assignment.SpoolID > 0 {
			spoolID = cloneIntPointer(&assignment.SpoolID)
		}
	} else {
		toolheadID, spoolID = b.getBestEffortHistoryTarget(checkpoint.PrinterName, config)
	}
	accountingKey := fmt.Sprintf("%s:%d:unknown", checkpoint.PrinterID, checkpoint.StartedAt.UnixNano())
	_, err := b.db.Exec(`INSERT OR IGNORE INTO print_history (
		printer_id, printer_name_at_event, toolhead_id, spool_id, filament_used,
		print_started, print_finished, job_name, source_path, print_state, accounting_key
	) VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?)`, checkpoint.PrinterID, checkpoint.PrinterName,
		toolheadID, spoolID, checkpoint.StartedAt, time.Now().UTC(), jobName, strings.TrimSpace(sourcePath), StateFinished, accountingKey)
	if err != nil {
		return fmt.Errorf("failed to log print without filament usage: %w", err)
	}

	if spoolID != nil {
		log.Printf("Logged completed print for %s on toolhead %d with spool %d but unknown filament weight",
			checkpoint.PrinterName, toolheadID, *spoolID)
	} else {
		log.Printf("Logged completed print for %s on toolhead %d with unknown spool and filament weight",
			checkpoint.PrinterName, toolheadID)
	}

	return nil
}

func (b *FilamentBridge) getBestEffortHistoryTarget(printerName string, config PrinterConfig) (int, *int) {
	toolheadCount := config.Toolheads
	if toolheadCount < 1 {
		toolheadCount = 1
	}

	if toolheadCount == 1 {
		spoolID, err := b.GetToolheadMapping(printerName, 0)
		if err == nil && spoolID > 0 {
			return 0, cloneIntPointer(&spoolID)
		}

		return 0, nil
	}

	mappedToolheadID := -1
	mappedSpoolID := 0
	for toolheadID := 0; toolheadID < toolheadCount; toolheadID++ {
		spoolID, err := b.GetToolheadMapping(printerName, toolheadID)
		if err != nil || spoolID <= 0 {
			continue
		}

		if mappedToolheadID != -1 {
			return 0, nil
		}

		mappedToolheadID = toolheadID
		mappedSpoolID = spoolID
	}

	if mappedToolheadID == -1 {
		return 0, nil
	}

	return mappedToolheadID, cloneIntPointer(&mappedSpoolID)
}

func cloneFilamentUsage(usage map[int]float64) map[int]float64 {
	if len(usage) == 0 {
		return nil
	}

	cloned := make(map[int]float64, len(usage))
	for toolheadID, weight := range usage {
		cloned[toolheadID] = weight
	}

	return cloned
}

func joinPrusaStoragePath(storagePath string, name string) string {
	storagePath = strings.Trim(strings.TrimSpace(storagePath), "/")
	name = strings.Trim(strings.TrimSpace(name), "/")

	switch {
	case storagePath == "":
		return name
	case name == "":
		return storagePath
	default:
		return storagePath + "/" + name
	}
}

func prusaPathBase(value string) string {
	value = strings.Trim(strings.TrimSpace(value), "/")
	if value == "" {
		return ""
	}

	parts := strings.Split(value, "/")
	return parts[len(parts)-1]
}

func resolvePrusaJobName(displayName string, name string, storagePath string) string {
	if displayName = strings.TrimSpace(displayName); displayName != "" {
		return displayName
	}
	if name = prusaPathBase(name); name != "" {
		return name
	}
	return prusaPathBase(storagePath)
}

// GetPrintErrors returns all unacknowledged print errors
func (b *FilamentBridge) GetPrintErrors() []PrintError {
	b.errorMutex.RLock()
	defer b.errorMutex.RUnlock()

	var errors []PrintError
	for _, err := range b.printErrors {
		if !err.Acknowledged {
			errors = append(errors, err)
		}
	}
	return errors
}

// AcknowledgePrintError marks a print error as acknowledged
func (b *FilamentBridge) AcknowledgePrintError(errorID string) error {
	b.errorMutex.Lock()
	defer b.errorMutex.Unlock()

	if err, exists := b.printErrors[errorID]; exists {
		err.Acknowledged = true
		b.printErrors[errorID] = err
		return nil
	}
	return fmt.Errorf("print error not found: %s", errorID)
}

// sanitizeErrorID replaces problematic characters in error IDs to make them URL-safe
func sanitizeErrorID(s string) string {
	// Replace forward slashes with underscores
	s = strings.ReplaceAll(s, "/", "_")
	// Replace spaces with underscores
	s = strings.ReplaceAll(s, " ", "_")
	// Replace backslashes with underscores
	s = strings.ReplaceAll(s, "\\", "_")
	return s
}

// addPrintError adds a new print error
func (b *FilamentBridge) addPrintError(printerName, filename, errorMsg string) {
	b.errorMutex.Lock()
	defer b.errorMutex.Unlock()

	// Sanitize printer name and filename to ensure URL-safe error IDs
	sanitizedPrinterName := sanitizeErrorID(printerName)
	sanitizedFilename := sanitizeErrorID(filename)
	errorID := fmt.Sprintf("%s_%s_%d", sanitizedPrinterName, sanitizedFilename, time.Now().Unix())
	b.printErrors[errorID] = PrintError{
		ID:           errorID,
		PrinterName:  printerName,
		Filename:     filename,
		Error:        errorMsg,
		Timestamp:    time.Now(),
		Acknowledged: false,
	}

	log.Printf("⚠️  Print processing failed for %s (%s): %s - Manual Spoolman update required",
		printerName, filename, errorMsg)
}

// GetStatus gets current status of all printers and mappings
func (b *FilamentBridge) GetStatus() (*PrinterStatus, error) {
	status := &PrinterStatus{
		Printers:         make(map[string]PrinterData),
		ToolheadMappings: make(map[string]map[int]ToolheadMapping),
		Timestamp:        time.Now(),
	}

	// Get a safe snapshot of the config to prevent iteration issues
	configSnapshot := b.GetConfigSnapshot()
	if configSnapshot == nil {
		// No printers configured
		status.Printers["no_printers"] = PrinterData{
			Name:  "No Printers Configured",
			State: StateNotConfigured,
		}
		return status, nil
	}

	// Read the current observations produced by the monitor cycle. Network I/O is
	// owned by that cycle so status handlers and WebSocket broadcasts cannot
	// double-poll a printer.
	if len(configSnapshot.Printers) > 0 {
		status.Printers = b.observations.snapshot(configSnapshot.Printers)
	} else {
		// No printers configured
		status.Printers["no_printers"] = PrinterData{
			Name:  "No Printers Configured",
			State: StateNotConfigured,
		}
	}

	// Get toolhead mappings for all printers
	for printerID, printerConfig := range configSnapshot.Printers {
		if printerID == "no_printers" {
			continue // Skip placeholder
		}

		printerName := printerConfig.Name
		mappings, err := b.GetToolheadMappings(printerID)
		if err != nil {
			log.Printf("Error getting toolhead mappings for %s: %v", printerName, err)
			mappings = make(map[int]ToolheadMapping)
		}

		// Get toolhead names for this printer
		toolheadNames, err := b.GetAllToolheadNames(printerID)
		if err != nil {
			log.Printf("Warning: Failed to get toolhead names for printer %s: %v", printerID, err)
			toolheadNames = make(map[int]string)
		}

		// Create enhanced mappings for ALL toolheads (including unmapped ones)
		enhancedMappings := make(map[int]ToolheadMapping)
		for toolheadID := 0; toolheadID < printerConfig.Toolheads; toolheadID++ {
			// Get display name (custom or default)
			var displayName string
			if name, exists := toolheadNames[toolheadID]; exists {
				displayName = name
			} else {
				displayName = fmt.Sprintf("Toolhead %d", toolheadID)
			}

			// If this toolhead has a mapping, use it and add display name
			if mapping, exists := mappings[toolheadID]; exists {
				mapping.DisplayName = displayName
				enhancedMappings[toolheadID] = mapping
			} else {
				// Create empty mapping with just display name for unmapped toolheads
				enhancedMappings[toolheadID] = ToolheadMapping{
					PrinterID:   printerID,
					PrinterName: printerName,
					ToolheadID:  toolheadID,
					SpoolID:     0, // No spool mapped
					DisplayName: displayName,
				}
			}
		}
		status.ToolheadMappings[printerID] = enhancedMappings
	}

	return status, nil
}

// processFilamentUsage processes filament usage updates for all toolheads
func (b *FilamentBridge) processFilamentUsage(printerName string, filamentUsage map[int]float64, jobName string, sourcePath string) error {
	identity, err := b.resolvePrinterReference(printerName)
	if err != nil {
		return err
	}
	return b.AccountCompletedPrint(CompletedPrintObservation{
		PrinterID:    identity.ID,
		JobName:      jobName,
		SourcePath:   sourcePath,
		FilamentUsed: filamentUsage,
		StartedAt:    time.Now().UTC(),
		PrintState:   StateFinished,
	})
}

func (b *FilamentBridge) processFilamentUsageWithCheckpoint(printerName string, filamentUsage map[int]float64, jobName string, sourcePath string, toolAssignments map[int]PrintToolAssignment, printState string, checkpoint *printerJobCheckpoint) error {
	if checkpoint == nil || toolAssignments == nil {
		return fmt.Errorf("durable print checkpoint and tool assignments are required")
	}
	defaultAuthority := ConsumptionAuthoritySpoolmanLed
	runtime := b.runtimeSnapshot()
	if runtime.config != nil {
		var err error
		defaultAuthority, err = ParseConsumptionAuthority(string(runtime.config.ConsumptionAuthority))
		if err != nil {
			return err
		}
	}

	logicalToolIDs := make([]int, 0, len(filamentUsage))
	for logicalToolID := range filamentUsage {
		logicalToolIDs = append(logicalToolIDs, logicalToolID)
	}
	sort.Ints(logicalToolIDs)

	// Update Spoolman with filament usage for each toolhead.
	for _, logicalToolID := range logicalToolIDs {
		usedWeight := filamentUsage[logicalToolID]
		if usedWeight <= 0 {
			continue
		}

		// Get the mapped spool for this toolhead
		toolheadID := logicalToolID
		spoolID := 0
		authority := defaultAuthority
		if assignment, ok := toolAssignments[logicalToolID]; ok {
			toolheadID = assignment.PhysicalToolheadID
			spoolID = assignment.SpoolID
			if assignment.Authority != "" {
				var err error
				authority, err = ParseConsumptionAuthority(string(assignment.Authority))
				if err != nil {
					return fmt.Errorf("invalid snapshotted consumption authority for logical tool %d: %w", logicalToolID, err)
				}
			} else {
				var err error
				authority, err = b.GetSpoolConsumptionAuthority(spoolID)
				if err != nil {
					return fmt.Errorf("failed to get consumption authority for spool %d: %w", spoolID, err)
				}
			}
		}
		if err := (ConsumptionUpdatePlan{
			Authority:              authority,
			AutomaticSpoolmanDebit: authority == ConsumptionAuthoritySpoolmanLed,
		}).Validate(); err != nil {
			return err
		}

		var historySpoolID *int
		if checkpoint != nil {
			if err := b.processDurableJobAdjustment(checkpoint, logicalToolID, toolheadID, spoolID, usedWeight, authority, jobName, sourcePath, printState); err != nil {
				return err
			}
			continue
		}
		if spoolID == 0 {
			log.Printf("No spool mapped to %s toolhead %d, logging history with unknown spool",
				printerName, toolheadID)
		} else {
			if authority == ConsumptionAuthoritySpoolmanLed {
				if err := runtime.spoolman.UpdateSpoolUsage(spoolID, usedWeight); err != nil {
					return fmt.Errorf("failed to update spool %d usage: %w", spoolID, err)
				}
			} else {
				log.Printf("Observed %.2fg on spool %d without debit; consumption authority is %s", usedWeight, spoolID, authority)
			}

			historySpoolID = cloneIntPointer(&spoolID)
		}

		// Log the usage in our database
		if err := b.logPrintUsageWithState(printerName, toolheadID, historySpoolID, usedWeight, jobName, sourcePath, printState); err != nil {
			return fmt.Errorf("failed to log print usage: %w", err)
		}

		if historySpoolID != nil {
			log.Printf("Updated spool %d: used %.2fg filament on %s toolhead %d",
				*historySpoolID, usedWeight, printerName, toolheadID)
			continue
		}

		log.Printf("Logged %.2fg filament on %s toolhead %d with unknown spool",
			usedWeight, printerName, toolheadID)
	}

	// Summary log
	if len(filamentUsage) > 0 {
		log.Printf("✅ Print completion processing finished for %s: processed %d toolheads", printerName, len(filamentUsage))
	} else {
		log.Printf("⚠️  No filament usage data processed for %s", printerName)
	}

	return nil
}

// isVirtualPrinterToolheadLocation checks if a location name matches the pattern
// of a virtual printer toolhead location (e.g., "PrinterName - Toolhead 0" or "PrinterName - Black")
func (b *FilamentBridge) isVirtualPrinterToolheadLocation(name string) bool {
	// Get all printer configurations
	printerConfigs, err := b.GetAllPrinterConfigs()
	if err != nil {
		// If we can't get printer configs, assume it's not a virtual location
		log.Printf("Warning: Could not get printer configurations to check virtual location: %v", err)
		return false
	}

	// Check if the name matches any printer's toolhead location pattern
	for printerID, printerConfig := range printerConfigs {
		// Get toolhead names for this printer
		toolheadNames, err := b.GetAllToolheadNames(printerID)
		if err != nil {
			log.Printf("Warning: Could not get toolhead names for printer %s: %v", printerID, err)
			toolheadNames = make(map[int]string)
		}

		for toolheadID := 0; toolheadID < printerConfig.Toolheads; toolheadID++ {
			// Check default pattern
			expectedNameDefault := fmt.Sprintf("%s - Toolhead %d", printerConfig.Name, toolheadID)
			if name == expectedNameDefault {
				return true
			}

			// Check custom name pattern
			if displayName, exists := toolheadNames[toolheadID]; exists {
				expectedNameCustom := fmt.Sprintf("%s - %s", printerConfig.Name, displayName)
				if name == expectedNameCustom {
					return true
				}
			}
		}
	}

	return false
}

// Close closes the database connection
func (b *FilamentBridge) Close() error {
	if b.db != nil {
		return b.db.Close()
	}
	return nil
}

// All FilaBridge location management functions have been removed - locations are now managed in Spoolman only
// REMOVED: CreateLocationFromSpoolman
// REMOVED: GetAllFilaBridgeLocations
// REMOVED: FindLocationByName
// REMOVED: UpdateLocation
// REMOVED: DeleteLocation
// REMOVED: GetLocationStatus
// REMOVED: LocationStatus struct
// REMOVED: AutoSyncSpoolmanLocations
// REMOVED: ImportSpoolmanLocations
// REMOVED: StartLocationSync
