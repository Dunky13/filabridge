package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// FilamentBridge manages the connection between PrusaLink and Spoolman
type FilamentBridge struct {
	config           *Config
	spoolman         *SpoolmanClient
	db               *sql.DB
	wasPrinting      map[string]bool
	currentJobFile   map[string]string // Store PrusaLink storage path per printer
	currentJobName   map[string]string // Store human-readable job name per printer
	currentJobUsage  map[string]map[int]float64
	processingPrints map[string]bool       // Track prints being processed
	printErrors      map[string]PrintError // Store print processing errors
	errorMutex       sync.RWMutex
	mutex            sync.RWMutex
}

// ToolheadMapping represents a mapping between a printer toolhead and a spool
type ToolheadMapping struct {
	PrinterName string    `json:"printer_name"`
	ToolheadID  int       `json:"toolhead_id"`
	SpoolID     int       `json:"spool_id"`
	MappedAt    time.Time `json:"mapped_at"`
	DisplayName string    `json:"display_name,omitempty"` // Custom toolhead name or empty for default
}

// PrintHistory represents a record of filament usage
type PrintHistory struct {
	ID            int       `json:"id"`
	PrinterName   string    `json:"printer_name"`
	ToolheadID    int       `json:"toolhead_id"`
	ToolheadName  string    `json:"toolhead_name,omitempty"`
	SpoolID       *int      `json:"spool_id"`
	FilamentUsed  float64   `json:"filament_used"`
	PrintStarted  time.Time `json:"print_started"`
	PrintFinished time.Time `json:"print_finished"`
	JobName       string    `json:"job_name"`
	SourcePath    string    `json:"source_path,omitempty"`
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
	bridge := &FilamentBridge{
		config:           config,
		spoolman:         NewSpoolmanClient(DefaultSpoolmanURL, SpoolmanTimeout, "", ""), // Default URL and timeout, will be updated
		wasPrinting:      make(map[string]bool),
		currentJobFile:   make(map[string]string),
		currentJobName:   make(map[string]string),
		currentJobUsage:  make(map[string]map[int]float64),
		processingPrints: make(map[string]bool),
		printErrors:      make(map[string]PrintError),
	}

	// Initialize database
	if err := bridge.initDatabase(); err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	// Update Spoolman URL and timeout if config is provided
	if config != nil && config.SpoolmanURL != "" {
		bridge.spoolman = NewSpoolmanClient(config.SpoolmanURL, config.SpoolmanTimeout, config.SpoolmanUsername, config.SpoolmanPassword)
	}

	return bridge, nil
}

// initDatabase initializes the SQLite database
func (b *FilamentBridge) initDatabase() error {
	dbFile := DefaultDBFileName
	if b.config != nil && b.config.DBFile != "" {
		dbFile = b.config.DBFile
	}
	// Check for environment variable (path only, append filename)
	if envDBPath := os.Getenv("FILABRIDGE_DB_PATH"); envDBPath != "" {
		dbFile = filepath.Join(envDBPath, DefaultDBFileName)
	}

	db, err := sql.Open("sqlite3", dbFile)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	b.db = db

	// Create tables
	createTables := []string{
		`CREATE TABLE IF NOT EXISTS configuration (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			description TEXT,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS printer_configs (
			printer_id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			model TEXT,
			ip_address TEXT NOT NULL,
			api_key TEXT,
			toolheads INTEGER DEFAULT 1,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS toolhead_mappings (
			printer_name TEXT,
			toolhead_id INTEGER,
			spool_id INTEGER,
			mapped_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (printer_name, toolhead_id)
		)`,
		`CREATE TABLE IF NOT EXISTS print_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			printer_name TEXT,
			toolhead_id INTEGER,
			spool_id INTEGER,
			filament_used REAL,
			print_started TIMESTAMP,
			print_finished TIMESTAMP,
			job_name TEXT,
			source_path TEXT,
			import_source TEXT NOT NULL DEFAULT 'runtime',
			external_job_id TEXT,
			external_lifetime_id TEXT,
			external_printer_uuid TEXT,
			print_state TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS nfc_sessions (
			session_id TEXT PRIMARY KEY,
			spool_id INTEGER,
			printer_name TEXT,
			toolhead_id INTEGER,
			location_name TEXT,
			is_printer_location BOOLEAN,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			expires_at TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS toolhead_names (
			printer_id TEXT,
			toolhead_id INTEGER,
			display_name TEXT NOT NULL,
			PRIMARY KEY (printer_id, toolhead_id)
		)`,
	}

	for _, query := range createTables {
		if _, err := b.db.Exec(query); err != nil {
			return fmt.Errorf("failed to create table: %w", err)
		}
	}

	if err := b.ensurePrintHistoryImportSchema(); err != nil {
		return fmt.Errorf("failed to migrate print history schema: %w", err)
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
		existingLocation, err := b.spoolman.FindLocationByName(locationName)
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
	for printerName, printerMappings := range allMappings {
		// Find the printer ID for this printer name
		var printerID string
		for pid, config := range printerConfigs {
			if config.Name == printerName {
				printerID = pid
				break
			}
		}

		if printerID == "" {
			log.Printf("Migration: Could not find printer ID for printer name '%s', skipping", printerName)
			continue
		}

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
			existingLocation, err := b.spoolman.FindLocationByName(locationName)
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
		ConfigKeyPollInterval:                    fmt.Sprintf("%d", DefaultPollInterval),
		ConfigKeyWebPort:                         DefaultWebPort,
		ConfigKeyPrusaLinkTimeout:                fmt.Sprintf("%d", PrusaLinkTimeout),
		ConfigKeyPrusaLinkFileDownloadTimeout:    fmt.Sprintf("%d", PrusaLinkFileDownloadTimeout),
		ConfigKeySpoolmanTimeout:                 fmt.Sprintf("%d", SpoolmanTimeout),
		ConfigKeyAutoAssignPreviousSpoolEnabled:  "false", // Enable auto-assignment of previous spool to default location
		ConfigKeyAutoAssignPreviousSpoolLocation: "",      // Default location name for auto-assigned previous spools
	}

	// Check if this is a fresh installation by checking if any config exists
	var totalCount int
	err := b.db.QueryRow("SELECT COUNT(*) FROM configuration").Scan(&totalCount)
	if err != nil {
		return fmt.Errorf("failed to check config existence: %w", err)
	}

	// Only insert defaults if this is a fresh installation
	if totalCount == 0 {
		for key, value := range defaultConfigs {
			_, err := b.db.Exec(
				"INSERT INTO configuration (key, value, description) VALUES (?, ?, ?)",
				key, value, getConfigDescription(key),
			)
			if err != nil {
				return fmt.Errorf("failed to insert default config %s: %w", key, err)
			}
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
	var spoolID int
	err := b.db.QueryRow(
		"SELECT spool_id FROM toolhead_mappings WHERE printer_name = ? AND toolhead_id = ?",
		printerName, toolheadID,
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
	locationName := strings.TrimSpace(explicitLocation)
	if locationName != "" {
		location, err := b.spoolman.FindLocationByName(locationName)
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

	location, err := b.spoolman.FindLocationByName(locationName)
	if err != nil {
		return "", fmt.Errorf("failed to validate auto-assign previous spool location '%s': %w", locationName, err)
	}
	if location == nil {
		return "", fmt.Errorf("auto-assign previous spool location '%s' does not exist", locationName)
	}

	return locationName, nil
}

func (b *FilamentBridge) getToolheadLocationName(printerName string, toolheadID int) string {
	printerConfigs, err := b.GetAllPrinterConfigs()
	if err != nil {
		return fmt.Sprintf("%s - Toolhead %d", printerName, toolheadID)
	}

	displayName := fmt.Sprintf("Toolhead %d", toolheadID)
	for printerID, printerConfig := range printerConfigs {
		if printerConfig.Name != printerName {
			continue
		}

		name, err := b.GetToolheadName(printerID, toolheadID)
		if err == nil {
			displayName = name
		}
		break
	}

	return fmt.Sprintf("%s - %s", printerName, displayName)
}

func (b *FilamentBridge) updateSpoolToolheadLocation(spoolID int, printerName string, toolheadID int) error {
	locationName := b.getToolheadLocationName(printerName, toolheadID)
	if _, err := b.spoolman.GetOrCreateLocation(locationName); err != nil {
		log.Printf("Warning: Failed to create/verify location '%s' in Spoolman: %v", locationName, err)
	}
	if err := b.spoolman.UpdateSpoolLocation(spoolID, locationName); err != nil {
		return fmt.Errorf("failed to update spool %d to toolhead location '%s': %w", spoolID, locationName, err)
	}
	return nil
}

func (b *FilamentBridge) setToolheadMappingRecord(printerName string, toolheadID int, spoolID int) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	rows, err := b.db.Query(
		"SELECT printer_name, toolhead_id FROM toolhead_mappings WHERE spool_id = ? AND NOT (printer_name = ? AND toolhead_id = ?)",
		spoolID, printerName, toolheadID,
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
		"INSERT OR REPLACE INTO toolhead_mappings (printer_name, toolhead_id, spool_id, mapped_at) VALUES (?, ?, ?, ?)",
		printerName, toolheadID, spoolID, time.Now(),
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
	rows, err := b.db.Query("SELECT printer_id, name, model, ip_address, api_key, toolheads FROM printer_configs")
	if err != nil {
		return nil, fmt.Errorf("failed to get printer configs: %w", err)
	}
	defer rows.Close()

	configs := make(map[string]PrinterConfig)
	for rows.Next() {
		var printerID, name, model, ipAddress, apiKey string
		var toolheads int
		if err := rows.Scan(&printerID, &name, &model, &ipAddress, &apiKey, &toolheads); err != nil {
			return nil, fmt.Errorf("failed to scan printer config row: %w", err)
		}
		configs[printerID] = PrinterConfig{
			Name:      name,
			Model:     model,
			IPAddress: ipAddress,
			APIKey:    apiKey,
			Toolheads: toolheads,
		}
	}

	return configs, nil
}

// SavePrinterConfig saves a printer configuration
func (b *FilamentBridge) SavePrinterConfig(printerID string, config PrinterConfig) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	_, err := b.db.Exec(`
		INSERT OR REPLACE INTO printer_configs (printer_id, name, model, ip_address, api_key, toolheads)
		VALUES (?, ?, ?, ?, ?, ?)
	`, printerID, config.Name, config.Model, config.IPAddress, config.APIKey, config.Toolheads)
	if err != nil {
		return fmt.Errorf("failed to save printer config: %w", err)
	}
	return nil
}

// DeletePrinterConfig deletes a printer configuration
func (b *FilamentBridge) DeletePrinterConfig(printerID string) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	_, err := b.db.Exec("DELETE FROM printer_configs WHERE printer_id = ?", printerID)
	if err != nil {
		return fmt.Errorf("failed to delete printer config: %w", err)
	}
	return nil
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
		// Get all spools from Spoolman
		spools, err := b.spoolman.GetAllSpools()
		if err != nil {
			log.Printf("Warning: Failed to get spools from Spoolman to update location names: %v", err)
		} else {
			// Find spools with the old location name and update them
			updatedCount := 0
			for _, spool := range spools {
				if spool.Location == oldLocationName {
					if err := b.spoolman.UpdateSpoolLocation(spool.ID, newLocationName); err != nil {
						log.Printf("Warning: Failed to update spool %d location from '%s' to '%s': %v", spool.ID, oldLocationName, newLocationName, err)
					} else {
						updatedCount++
					}
				}
			}

			// Ensure the new location exists in Spoolman
			if _, err := b.spoolman.GetOrCreateLocation(newLocationName); err != nil {
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
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	// Return a copy of the config to prevent iteration issues during updates
	if b.config == nil {
		return nil
	}

	// Create a shallow copy of the config
	configCopy := &Config{
		SpoolmanURL:                  b.config.SpoolmanURL,
		PollInterval:                 b.config.PollInterval,
		DBFile:                       b.config.DBFile,
		WebPort:                      b.config.WebPort,
		PrusaLinkTimeout:             b.config.PrusaLinkTimeout,
		PrusaLinkFileDownloadTimeout: b.config.PrusaLinkFileDownloadTimeout,
		SpoolmanTimeout:              b.config.SpoolmanTimeout,
		Printers:                     make(map[string]PrinterConfig),
	}

	// Copy printer configs
	for id, printer := range b.config.Printers {
		configCopy.Printers[id] = printer
	}

	return configCopy
}

// ReloadConfig reloads the configuration from the database
func (b *FilamentBridge) ReloadConfig() error {
	// Load config outside the lock to minimize lock time
	config, err := LoadConfig(b)
	if err != nil {
		return fmt.Errorf("failed to reload config: %w", err)
	}

	// Only lock briefly to swap the config pointer and recreate SpoolmanClient
	b.mutex.Lock()
	b.config = config
	if config.SpoolmanURL != "" {
		b.spoolman = NewSpoolmanClient(config.SpoolmanURL, config.SpoolmanTimeout, config.SpoolmanUsername, config.SpoolmanPassword)
	}
	b.mutex.Unlock()

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
	b.mutex.Lock()
	defer b.mutex.Unlock()

	b.config = config
	b.spoolman = NewSpoolmanClient(config.SpoolmanURL, config.SpoolmanTimeout, config.SpoolmanUsername, config.SpoolmanPassword)

	return nil
}

// GetToolheadMapping gets spool ID mapped to a specific toolhead
func (b *FilamentBridge) GetToolheadMapping(printerName string, toolheadID int) (int, error) {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	var spoolID int
	err := b.db.QueryRow(
		"SELECT spool_id FROM toolhead_mappings WHERE printer_name = ? AND toolhead_id = ?",
		printerName, toolheadID,
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
			// Verify the location exists in Spoolman
			location, err := b.spoolman.FindLocationByName(locationName)
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
	rows, err := b.db.Query(
		"SELECT toolhead_id, spool_id, mapped_at FROM toolhead_mappings WHERE printer_name = ?",
		printerName,
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
			PrinterName: printerName,
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
		"SELECT printer_name, toolhead_id, spool_id, mapped_at FROM toolhead_mappings ORDER BY printer_name, toolhead_id",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	mappings := make(map[string]map[int]ToolheadMapping)
	for rows.Next() {
		var printerName string
		var toolheadID, spoolID int
		var mappedAt time.Time
		if err := rows.Scan(&printerName, &toolheadID, &spoolID, &mappedAt); err != nil {
			return nil, err
		}

		if mappings[printerName] == nil {
			mappings[printerName] = make(map[int]ToolheadMapping)
		}

		mappings[printerName][toolheadID] = ToolheadMapping{
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
	b.mutex.Lock()
	defer b.mutex.Unlock()

	_, err := b.db.Exec(
		"DELETE FROM toolhead_mappings WHERE printer_name = ? AND toolhead_id = ?",
		printerName, toolheadID,
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
	b.mutex.Lock()
	defer b.mutex.Unlock()

	// Get print start time from current job file tracking
	printStarted := time.Now() // Default to now if we can't determine start time
	if storedJobFile, exists := b.currentJobFile[printerName]; exists && storedJobFile != "" {
		// If we have a stored job file, the print likely started when we first stored it
		// This is a rough approximation - ideally we'd track this more precisely
		printStarted = time.Now().Add(-time.Hour) // Assume 1 hour ago as rough estimate
	}

	_, err := b.db.Exec(
		"INSERT INTO print_history (printer_name, toolhead_id, spool_id, filament_used, print_started, print_finished, job_name, source_path) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		printerName, toolheadID, spoolID, filamentUsed, printStarted, time.Now(), jobName, strings.TrimSpace(sourcePath),
	)
	if err != nil {
		return fmt.Errorf("failed to log print usage: %w", err)
	}

	return nil
}

func (b *FilamentBridge) getToolheadDisplayName(printerName string, toolheadID int) string {
	printerConfigs, err := b.GetAllPrinterConfigs()
	if err != nil {
		return fmt.Sprintf("Toolhead %d", toolheadID)
	}

	for printerID, printerConfig := range printerConfigs {
		if printerConfig.Name != printerName {
			continue
		}

		name, err := b.GetToolheadName(printerID, toolheadID)
		if err == nil {
			return name
		}

		break
	}

	return fmt.Sprintf("Toolhead %d", toolheadID)
}

// GetPrintHistory returns latest print history entries.
func (b *FilamentBridge) GetPrintHistory(limit int) ([]PrintHistory, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := b.db.Query(`
		SELECT id, printer_name, toolhead_id, spool_id, filament_used, print_started, print_finished, job_name, source_path
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
			&entry.PrinterName,
			&entry.ToolheadID,
			&spoolID,
			&entry.FilamentUsed,
			&entry.PrintStarted,
			&entry.PrintFinished,
			&entry.JobName,
			&sourcePath,
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

		entry.ToolheadName = b.getToolheadDisplayName(entry.PrinterName, entry.ToolheadID)
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
		SELECT id, printer_name, toolhead_id, spool_id, filament_used, print_started, print_finished, job_name, source_path
		FROM print_history
		WHERE id = ?
	`, historyID).Scan(
		&entry.ID,
		&entry.PrinterName,
		&entry.ToolheadID,
		&spoolID,
		&entry.FilamentUsed,
		&entry.PrintStarted,
		&entry.PrintFinished,
		&entry.JobName,
		&sourcePath,
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

	entry.ToolheadName = b.getToolheadDisplayName(entry.PrinterName, entry.ToolheadID)
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
	printerConfigs, err := b.GetAllPrinterConfigs()
	if err != nil {
		return "", PrinterConfig{}, fmt.Errorf("failed to get printer configs: %w", err)
	}

	for printerID, printerConfig := range printerConfigs {
		if printerConfig.Name == printerName {
			return printerID, printerConfig, nil
		}
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

	_, printerConfig, err := b.getPrinterConfigByName(entry.PrinterName)
	if err != nil {
		return nil, err
	}

	prusaClient := NewPrusaLinkClient(printerConfig.IPAddress, printerConfig.APIKey, b.config.PrusaLinkTimeout, b.config.PrusaLinkFileDownloadTimeout)

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

	if spoolID != nil {
		if _, err := b.spoolman.GetSpool(*spoolID); err != nil {
			return err
		}
	}

	if entry.SpoolID != nil && entry.FilamentUsed > 0 {
		if err := b.spoolman.AdjustSpoolUsage(*entry.SpoolID, -entry.FilamentUsed); err != nil {
			return fmt.Errorf("failed to revert usage from spool %d: %w", *entry.SpoolID, err)
		}
	}

	if spoolID != nil && filamentUsed > 0 {
		if err := b.spoolman.AdjustSpoolUsage(*spoolID, filamentUsed); err != nil {
			if entry.SpoolID != nil && entry.FilamentUsed > 0 {
				rollbackErr := b.spoolman.AdjustSpoolUsage(*entry.SpoolID, entry.FilamentUsed)
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
			rollbackNewErr := b.spoolman.AdjustSpoolUsage(*spoolID, -filamentUsed)
			if rollbackNewErr != nil {
				log.Printf("Failed to rollback new spool usage for entry %d: %v", historyID, rollbackNewErr)
			}
		}
		if entry.SpoolID != nil && entry.FilamentUsed > 0 {
			rollbackOldErr := b.spoolman.AdjustSpoolUsage(*entry.SpoolID, entry.FilamentUsed)
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
	log.Printf("Monitoring printers at %s", time.Now().Format(time.RFC3339))

	// Get a safe snapshot of the config to prevent iteration issues
	configSnapshot := b.GetConfigSnapshot()
	if configSnapshot == nil || len(configSnapshot.Printers) == 0 {
		log.Printf("No printers configured - skipping monitoring")
		return
	}

	// Monitor each printer using PrusaLink
	for printerID, printerConfig := range configSnapshot.Printers {
		if printerID == "no_printers" {
			continue // Skip placeholder
		}
		go func(printerID string, config PrinterConfig) {
			if err := b.monitorPrusaLink(printerID, config); err != nil {
				log.Printf("Error monitoring printer %s (%s): %v", config.IPAddress, printerID, err)
			}
		}(printerID, printerConfig)
	}
}

// monitorPrusaLink monitors a single printer using PrusaLink API
func (b *FilamentBridge) monitorPrusaLink(printerID string, config PrinterConfig) error {
	log.Printf("Starting monitoring for printer %s (%s) at %s", printerID, config.IPAddress, config.Name)
	client := NewPrusaLinkClient(config.IPAddress, config.APIKey, b.config.PrusaLinkTimeout, b.config.PrusaLinkFileDownloadTimeout)

	status, err := client.GetStatus()
	if err != nil {
		log.Printf("Warning: Failed to get printer status from %s (%s): %v", config.IPAddress, printerID, err)
		return nil // Don't fail the entire monitoring cycle for one printer
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

	// Check if print just finished - minimize lock scope
	b.mutex.RLock()
	wasPrinting := b.wasPrinting[printerID]
	storedJobFile := b.currentJobFile[printerID]
	storedJobName := b.currentJobName[printerID]
	storedJobUsage := cloneFilamentUsage(b.currentJobUsage[printerID])
	b.mutex.RUnlock()

	// Debug logging for all printers
	log.Printf("Printer %s (%s): state=%s, wasPrinting=%v, job=%s, stored_file=%s",
		config.IPAddress, printerID, currentState, wasPrinting, jobName, storedJobFile)

	// Check if print just finished
	if (currentState == StateIdle || currentState == StateFinished) && wasPrinting {
		// Use stored filename (should be available since we stored it when printing started)
		filenameToUse := storedJobFile
		jobNameToUse := storedJobName
		filamentUsageToUse := currentJobUsage
		if len(filamentUsageToUse) == 0 {
			filamentUsageToUse = storedJobUsage
		}
		if filenameToUse == "" {
			log.Printf("Warning: No stored filename for %s (%s), using current job filename: %s",
				config.IPAddress, printerID, currentJobFilename)
			filenameToUse = currentJobFilename
		}
		if jobNameToUse == "" {
			jobNameToUse = currentJobDisplayName
		}
		if jobNameToUse == "" {
			jobNameToUse = resolvePrusaJobName("", "", filenameToUse)
		}

		log.Printf("🎉 Print finished detected for %s (%s): %s (state: %s, file: %s)",
			config.IPAddress, printerID, jobNameToUse, currentState, filenameToUse)

		// Mark as processing to prevent filename from being cleared
		b.mutex.Lock()
		b.wasPrinting[printerID] = false
		b.processingPrints[printerID] = true
		b.mutex.Unlock()

		// Now process the print (this takes a long time)
		err := b.handlePrusaLinkPrintFinished(config, filenameToUse, jobNameToUse, filamentUsageToUse)

		// Clear processing flag and filename after completion
		b.mutex.Lock()
		b.processingPrints[printerID] = false
		if err == nil {
			b.currentJobFile[printerID] = ""
			delete(b.currentJobName, printerID)
			delete(b.currentJobUsage, printerID)
		}
		b.mutex.Unlock()

		if err != nil {
			log.Printf("Error handling PrusaLink print finished: %v", err)
		}
	} else {
		// Update state tracking - minimize lock scope
		b.mutex.Lock()
		defer b.mutex.Unlock()

		// Store the current job filename when printing starts (only if not already stored)
		if currentState == StatePrinting && currentJobFilename != "" {
			if storedJobFile == "" {
				b.currentJobFile[printerID] = currentJobFilename
				log.Printf("📁 Stored job storage path for %s (%s): %s", config.IPAddress, printerID, currentJobFilename)
			}
			if storedJobName == "" && currentJobDisplayName != "" {
				b.currentJobName[printerID] = currentJobDisplayName
				log.Printf("📝 Stored job display name for %s (%s): %s", config.IPAddress, printerID, currentJobDisplayName)
			}
		}
		if currentState == StatePrinting && len(currentJobUsage) > 0 {
			b.currentJobUsage[printerID] = cloneFilamentUsage(currentJobUsage)
		}

		// Update wasPrinting flag for NEXT cycle
		b.wasPrinting[printerID] = currentState == StatePrinting

		// Clear stored filename when print finishes (but only if not currently processing)
		if (currentState == StateIdle || currentState == StateFinished) && !b.processingPrints[printerID] {
			b.currentJobFile[printerID] = ""
			delete(b.currentJobName, printerID)
			delete(b.currentJobUsage, printerID)
		}
	}

	return nil
}

// handlePrusaLinkPrintFinished handles when a print job finishes via PrusaLink
func (b *FilamentBridge) handlePrusaLinkPrintFinished(config PrinterConfig, storagePath string, jobName string, filamentUsage map[int]float64) error {
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

		prusaClient := NewPrusaLinkClient(config.IPAddress, config.APIKey, b.config.PrusaLinkTimeout, b.config.PrusaLinkFileDownloadTimeout)
		resolvedUsage, err := prusaClient.GetFilamentUsageForFile(storagePath)
		if err != nil {
			errorMsg := fmt.Sprintf("failed to extract filament usage from PrusaLink: %v", err)
			return b.logPrintHistoryWithoutUsage(printerName, config, storagePath, jobName, errorMsg)
		}

		filamentUsage = resolvedUsage
	}

	// Check if we got any filament usage data
	if len(filamentUsage) == 0 {
		errorMsg := "no filament usage data found in PrusaLink metadata"
		return b.logPrintHistoryWithoutUsage(printerName, config, storagePath, jobName, errorMsg)
	}

	log.Printf("Successfully collected filament usage: %+v", filamentUsage)

	// Process filament usage using helper function
	if err := b.processFilamentUsage(printerName, filamentUsage, jobName, storagePath); err != nil {
		log.Printf("Error processing filament usage: %v", err)
		return err
	}

	return nil
}

func (b *FilamentBridge) logPrintHistoryWithoutUsage(printerName string, config PrinterConfig, sourcePath string, jobName string, errorMsg string) error {
	b.addPrintError(printerName, jobName, errorMsg)

	toolheadID, spoolID := b.getBestEffortHistoryTarget(printerName, config)
	if err := b.LogPrintUsageWithSourcePath(printerName, toolheadID, spoolID, 0, jobName, sourcePath); err != nil {
		return fmt.Errorf("failed to log print without filament usage: %w", err)
	}

	if spoolID != nil {
		log.Printf("Logged completed print for %s on toolhead %d with spool %d but unknown filament weight",
			printerName, toolheadID, *spoolID)
	} else {
		log.Printf("Logged completed print for %s on toolhead %d with unknown spool and filament weight",
			printerName, toolheadID)
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

	// Get printer statuses from PrusaLink
	if len(configSnapshot.Printers) > 0 {
		for printerID, printerConfig := range configSnapshot.Printers {
			if printerID == "no_printers" {
				continue // Skip placeholder
			}

			client := NewPrusaLinkClient(printerConfig.IPAddress, printerConfig.APIKey, b.config.PrusaLinkTimeout, b.config.PrusaLinkFileDownloadTimeout)

			// Use the configured printer name, not the hostname from PrusaLink
			printerName := printerConfig.Name

			// Get current status
			printerStatus, err := client.GetStatus()
			if err != nil {
				// Enhanced error logging to help diagnose connection issues
				// This is especially useful for DNS resolution problems with hostnames
				log.Printf("Warning: Failed to get printer status from %s (%s - %s): %v",
					printerConfig.IPAddress, printerID, printerName, err)
				status.Printers[printerID] = PrinterData{
					Name:  printerName,
					State: StateOffline,
				}
				continue
			}

			printerData := PrinterData{
				Name:          printerName,
				State:         printerStatus.Printer.State,
				Progress:      printerStatus.Job.Progress,
				PrintTime:     printerStatus.Job.TimePrinting,
				PrintTimeLeft: printerStatus.Job.TimeRemaining,
			}

			hasActiveJob := printerData.State == StatePrinting ||
				printerData.Progress > 0 ||
				printerData.PrintTime > 0 ||
				printerData.PrintTimeLeft > 0

			if hasActiveJob {
				jobInfo, err := client.GetJobInfo()
				if err != nil {
					log.Printf("Warning: Failed to get printer job info from %s (%s - %s): %v",
						printerConfig.IPAddress, printerID, printerName, err)
				} else if jobInfo.File.DisplayName != "" {
					printerData.CurrentJob = jobInfo.File.DisplayName
				} else if jobInfo.File.Name != "" {
					printerData.CurrentJob = jobInfo.File.Name
				}
			}

			status.Printers[printerID] = printerData
		}
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
		mappings, err := b.GetToolheadMappings(printerName)
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
	// Update Spoolman with filament usage for each toolhead
	for toolheadID, usedWeight := range filamentUsage {
		if usedWeight <= 0 {
			continue
		}

		// Get the mapped spool for this toolhead
		spoolID, err := b.GetToolheadMapping(printerName, toolheadID)
		if err != nil {
			log.Printf("Error getting toolhead mapping for %s toolhead %d: %v",
				printerName, toolheadID, err)
			continue
		}

		var historySpoolID *int
		if spoolID == 0 {
			log.Printf("No spool mapped to %s toolhead %d, logging history with unknown spool",
				printerName, toolheadID)
		} else {
			// Update Spoolman
			if err := b.spoolman.UpdateSpoolUsage(spoolID, usedWeight); err != nil {
				log.Printf("Error updating spool %d usage: %v", spoolID, err)
				continue
			}

			historySpoolID = cloneIntPointer(&spoolID)
		}

		// Log the usage in our database
		if err := b.LogPrintUsageWithSourcePath(printerName, toolheadID, historySpoolID, usedWeight, jobName, sourcePath); err != nil {
			log.Printf("Error logging print usage: %v", err)
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
