package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestConcurrentBridgeInitializationSerializesSchemaMigrations(t *testing.T) {
	dbFile := filepath.Join(t.TempDir(), "concurrent.db")
	const initializerCount = 32

	start := make(chan struct{})
	errors := make(chan error, initializerCount)
	var workers sync.WaitGroup
	workers.Add(initializerCount)
	for range initializerCount {
		go func() {
			defer workers.Done()
			<-start
			bridge, err := NewFilamentBridge(&Config{DBFile: dbFile})
			if err == nil {
				err = bridge.Close()
			}
			errors <- err
		}()
	}
	close(start)
	workers.Wait()
	close(errors)

	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent NewFilamentBridge() error = %v", err)
		}
	}

	db, err := sql.Open("sqlite3", sqliteDSN(dbFile))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var migrationCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != len(schemaMigrations) {
		t.Fatalf("schema migration ledger count = %d, want %d", migrationCount, len(schemaMigrations))
	}
}

func TestForeignKeysAreEnabledOnEveryDatabaseConnection(t *testing.T) {
	spoolman := newTestSpoolmanServer()
	defer spoolman.close()
	bridge := newTestBridge(t, spoolman.server.URL)
	bridge.db.SetMaxOpenConns(3)

	connections := make([]*sql.Conn, 0, 3)
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for index := 0; index < 3; index++ {
		connection, err := bridge.db.Conn(context.Background())
		if err != nil {
			t.Fatalf("open connection %d: %v", index, err)
		}
		connections = append(connections, connection)
		var enabled int
		if err := connection.QueryRowContext(context.Background(), "PRAGMA foreign_keys").Scan(&enabled); err != nil {
			t.Fatalf("read foreign_keys on connection %d: %v", index, err)
		}
		if enabled != 1 {
			t.Fatalf("foreign_keys on connection %d = %d, want 1", index, enabled)
		}
	}
}

func TestStablePrinterRelationsDeclareTheirDeletePolicy(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()
	bridge := newTestBridge(t, spoolman.server.URL)

	want := map[string]string{
		"toolhead_mappings":       "CASCADE",
		"nfc_sessions":            "CASCADE",
		"toolhead_names":          "CASCADE",
		"logical_tool_routes":     "CASCADE",
		"printer_job_checkpoints": "CASCADE",
		"printer_job_adjustments": "CASCADE",
		"print_history":           "SET NULL",
	}
	for table, wantDelete := range want {
		gotDelete, err := printerForeignKeyDeleteAction(bridge.db, table)
		if err != nil {
			t.Fatalf("inspect %s foreign key: %v", table, err)
		}
		if gotDelete != wantDelete {
			t.Fatalf("%s printer foreign key delete action = %q, want %q", table, gotDelete, wantDelete)
		}
	}
}

func TestPrintAccountingUsesDurableCheckpointAndExactStartTime(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()
	bridge := newTestBridge(t, spoolman.server.URL)
	if err := bridge.SetToolheadMapping("printer-a", 7, 20); err != nil {
		t.Fatalf("SetToolheadMapping() error = %v", err)
	}
	if err := bridge.SetLogicalToolRoute("printer-a", 0, 7); err != nil {
		t.Fatalf("SetLogicalToolRoute() error = %v", err)
	}
	startedAt := time.Date(2026, time.September, 4, 8, 9, 10, 0, time.UTC)
	if err := bridge.AccountCompletedPrint(CompletedPrintObservation{
		PrinterID:    "printer-a",
		JobName:      "exact.bgcode",
		SourcePath:   "usb/exact.bgcode",
		FilamentUsed: map[int]float64{0: 12.5},
		StartedAt:    startedAt,
		PrintState:   StateFinished,
	}); err != nil {
		t.Fatalf("AccountCompletedPrint() error = %v", err)
	}

	history, err := bridge.GetPrintHistory(10)
	if err != nil || len(history) != 1 {
		t.Fatalf("GetPrintHistory() = %#v, %v", history, err)
	}
	if !history[0].PrintStarted.Equal(startedAt) || history[0].PrinterID != "printer-a" || history[0].ToolheadID != 7 {
		t.Fatalf("history = %#v, want stable printer, physical tool 7, and exact start %s", history[0], startedAt)
	}
	var status string
	if err := bridge.db.QueryRow(`SELECT status FROM printer_job_adjustments WHERE printer_id = ? AND logical_tool_id = 0`, "printer-a").Scan(&status); err != nil {
		t.Fatalf("read durable adjustment: %v", err)
	}
	if status != jobAccountingCompleted {
		t.Fatalf("adjustment status = %q, want completed", status)
	}
}

func TestPrinterRegistryKeepsLiveRelationsAcrossRenameAndDelete(t *testing.T) {
	spoolman := newTestSpoolmanServer()
	defer spoolman.close()
	bridge := newTestBridge(t, spoolman.server.URL)

	printer := PrinterConfig{Name: "Before", Model: ModelCoreOne, IPAddress: "printer.local", Toolheads: 1}
	if err := bridge.SavePrinterConfig("printer-a", printer); err != nil {
		t.Fatalf("SavePrinterConfig(before) error = %v", err)
	}
	if err := bridge.SetToolheadMapping("Before", 0, 20); err != nil {
		t.Fatalf("SetToolheadMapping() error = %v", err)
	}
	if _, err := bridge.createOrUpdateSession("session-a", 20, "Before", 0, "Before - Toolhead 0", true); err != nil {
		t.Fatalf("createOrUpdateSession() error = %v", err)
	}

	printer.Name = "After"
	if err := bridge.SavePrinterConfig("printer-a", printer); err != nil {
		t.Fatalf("SavePrinterConfig(after) error = %v", err)
	}
	spoolID, err := bridge.GetToolheadMapping("After", 0)
	if err != nil || spoolID != 20 {
		t.Fatalf("GetToolheadMapping(after rename) = %d, %v; want 20", spoolID, err)
	}
	session, err := bridge.getSession("session-a")
	if err != nil {
		t.Fatalf("getSession() error = %v", err)
	}
	if session.PrinterName != "After" || session.LocationName != "After - Toolhead 0" {
		t.Fatalf("session after rename = %#v, want current printer and location display", session)
	}
	if got := spoolman.spoolLocation(20); got != "After - Toolhead 0" {
		t.Fatalf("spool location after printer rename = %q, want %q", got, "After - Toolhead 0")
	}

	if err := bridge.DeletePrinterConfig("printer-a"); err != nil {
		t.Fatalf("DeletePrinterConfig() error = %v", err)
	}
	var mappingCount, sessionCount int
	if err := bridge.db.QueryRow("SELECT COUNT(*) FROM toolhead_mappings WHERE printer_id = ?", "printer-a").Scan(&mappingCount); err != nil {
		t.Fatalf("count mappings: %v", err)
	}
	if err := bridge.db.QueryRow("SELECT COUNT(*) FROM nfc_sessions WHERE printer_id = ?", "printer-a").Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if mappingCount != 0 || sessionCount != 0 {
		t.Fatalf("delete left live relations: mappings=%d sessions=%d", mappingCount, sessionCount)
	}
}

func TestLegacyMigrationPreservesStableIdentityAndHistoryDisplay(t *testing.T) {
	dbFile := filepath.Join(t.TempDir(), "legacy.db")
	legacy := openLegacyIdentityFixture(t, dbFile)
	if _, err := legacy.Exec(`INSERT INTO printer_configs (printer_id, name, model, ip_address, toolheads) VALUES ('printer-a', 'Before', 'MK4S', 'printer.local', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO toolhead_mappings (printer_name, toolhead_id, spool_id, mapped_at) VALUES ('Before', 0, 20, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO print_history (printer_name, toolhead_id, spool_id, filament_used, print_started, print_finished, job_name) VALUES ('Before', 0, 20, 12.5, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'part.bgcode')`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO nfc_sessions (session_id, spool_id, printer_name, toolhead_id, location_name, is_printer_location, created_at, expires_at) VALUES ('session-a', 20, 'Before', 0, 'Before - Toolhead 0', 1, CURRENT_TIMESTAMP, ?)`, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	bridge, err := NewFilamentBridge(&Config{DBFile: dbFile, SpoolmanURL: "http://127.0.0.1:1", SpoolmanTimeout: 1})
	if err != nil {
		t.Fatalf("NewFilamentBridge() migration error = %v", err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	var migrationCount int
	if err := bridge.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatalf("read schema migration ledger: %v", err)
	}
	if migrationCount != len(schemaMigrations) {
		t.Fatalf("schema migration ledger count = %d, want %d", migrationCount, len(schemaMigrations))
	}

	if got, err := bridge.GetToolheadMapping("Before", 0); err != nil || got != 20 {
		t.Fatalf("migrated mapping = %d, %v; want 20", got, err)
	}
	printer := PrinterConfig{Name: "After", Model: ModelCoreOne, IPAddress: "printer.local", Toolheads: 1}
	if err := bridge.SavePrinterConfig("printer-a", printer); err != nil {
		t.Fatalf("rename migrated printer: %v", err)
	}
	history, err := bridge.GetPrintHistory(10)
	if err != nil || len(history) != 1 {
		t.Fatalf("GetPrintHistory() = %#v, %v", history, err)
	}
	if history[0].PrinterName != "Before" {
		t.Fatalf("history display = %q, want immutable Before", history[0].PrinterName)
	}
	if got, err := bridge.GetToolheadMapping("After", 0); err != nil || got != 20 {
		t.Fatalf("mapping after rename = %d, %v; want 20", got, err)
	}
	if err := bridge.DeletePrinterConfig("printer-a"); err != nil {
		t.Fatalf("DeletePrinterConfig() error = %v", err)
	}
	history, err = bridge.GetPrintHistory(10)
	if err != nil || len(history) != 1 || history[0].PrinterID != "" || history[0].PrinterName != "Before" {
		t.Fatalf("history after printer deletion = %#v, %v; want preserved event with detached live ID", history, err)
	}
}

func TestLegacyMigrationRejectsAmbiguousPrinterNameRelations(t *testing.T) {
	dbFile := filepath.Join(t.TempDir(), "ambiguous.db")
	legacy := openLegacyIdentityFixture(t, dbFile)
	for _, printerID := range []string{"printer-a", "printer-b"} {
		if _, err := legacy.Exec(`INSERT INTO printer_configs (printer_id, name, model, ip_address, toolheads) VALUES (?, 'Shared', 'MK4S', ?, 1)`, printerID, printerID+".local"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := legacy.Exec(`INSERT INTO toolhead_mappings (printer_name, toolhead_id, spool_id, mapped_at) VALUES ('Shared', 0, 20, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := NewFilamentBridge(&Config{DBFile: dbFile})
	if err == nil || !strings.Contains(err.Error(), `ambiguous legacy printer name "Shared"`) {
		t.Fatalf("NewFilamentBridge() error = %v, want explicit ambiguous-name migration failure", err)
	}
	rolledBack, err := sql.Open("sqlite3", sqliteDSN(dbFile))
	if err != nil {
		t.Fatal(err)
	}
	defer rolledBack.Close()
	var applied int
	if err := rolledBack.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = 2").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Fatalf("failed stable-identity migration was recorded as applied")
	}
	columns, err := tableColumns(rolledBack, "toolhead_mappings")
	if err != nil {
		t.Fatal(err)
	}
	if !columns["printer_name"] || columns["printer_id"] {
		t.Fatalf("failed migration left partial toolhead schema: %#v", columns)
	}
}

func openLegacyIdentityFixture(t *testing.T, filename string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filename)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE printer_configs (printer_id TEXT PRIMARY KEY, name TEXT NOT NULL, model TEXT, ip_address TEXT NOT NULL, api_key TEXT, toolheads INTEGER DEFAULT 1, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE toolhead_mappings (printer_name TEXT, toolhead_id INTEGER, spool_id INTEGER, mapped_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (printer_name, toolhead_id))`,
		`CREATE TABLE print_history (id INTEGER PRIMARY KEY AUTOINCREMENT, printer_name TEXT, toolhead_id INTEGER, spool_id INTEGER, filament_used REAL, print_started TIMESTAMP, print_finished TIMESTAMP, job_name TEXT)`,
		`CREATE TABLE nfc_sessions (session_id TEXT PRIMARY KEY, spool_id INTEGER, printer_name TEXT, toolhead_id INTEGER, location_name TEXT, is_printer_location BOOLEAN, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, expires_at TIMESTAMP)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var id, notNull, primaryKey int
		var name, columnType string
		var defaultValue interface{}
		if err := rows.Scan(&id, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func printerForeignKeyDeleteAction(db *sql.DB, table string) (string, error) {
	rows, err := db.Query("PRAGMA foreign_key_list(" + table + ")")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var id, seq int
		var target, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &target, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return "", err
		}
		if target == "printer_configs" && from == "printer_id" {
			return onDelete, nil
		}
	}
	return "", rows.Err()
}
