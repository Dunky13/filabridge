package main

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

type schemaMigration struct {
	Version int
	Name    string
	Apply   func(schemaMigrationExecutor) error
}

type schemaMigrationExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

type dedicatedSchemaMigrationExecutor struct {
	connection *sql.Conn
}

func (executor dedicatedSchemaMigrationExecutor) Exec(query string, args ...any) (sql.Result, error) {
	return executor.connection.ExecContext(context.Background(), query, args...)
}

func (executor dedicatedSchemaMigrationExecutor) Query(query string, args ...any) (*sql.Rows, error) {
	return executor.connection.QueryContext(context.Background(), query, args...)
}

func (executor dedicatedSchemaMigrationExecutor) QueryRow(query string, args ...any) *sql.Row {
	return executor.connection.QueryRowContext(context.Background(), query, args...)
}

var schemaMigrations = []schemaMigration{
	{Version: 1, Name: "legacy schema baseline", Apply: migrateLegacySchemaBaseline},
	{Version: 2, Name: "stable printer identity", Apply: migrateStablePrinterIdentity},
	{Version: 3, Name: "printer accounting foreign keys", Apply: migratePrinterAccountingForeignKeys},
	{Version: 4, Name: "normalize imported job lifetime identity", Apply: normalizeImportedJobLifetimeIdentity},
}

func sqliteDSN(filename string) string {
	separator := "?"
	if strings.Contains(filename, "?") {
		separator = "&"
	}
	return filename + separator + "_foreign_keys=on&_busy_timeout=5000"
}

func runSchemaMigrations(db *sql.DB) error {
	connection, err := db.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("open schema migration connection: %w", err)
	}
	defer connection.Close()

	if err := runImmediateSchemaTransaction(connection, func(executor schemaMigrationExecutor) error {
		_, err := executor.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TIMESTAMP NOT NULL
		)`)
		return err
	}); err != nil {
		return fmt.Errorf("create schema migration ledger: %w", err)
	}

	migrations := append([]schemaMigration(nil), schemaMigrations...)
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	for index, migration := range migrations {
		want := index + 1
		if migration.Version != want {
			return fmt.Errorf("schema migrations must be contiguous: position %d has version %d", want, migration.Version)
		}
	}
	for _, migration := range migrations {
		if err := runImmediateSchemaTransaction(connection, func(executor schemaMigrationExecutor) error {
			var applied int
			if err := executor.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", migration.Version).Scan(&applied); err != nil {
				return fmt.Errorf("read schema migration %d: %w", migration.Version, err)
			}
			if applied != 0 {
				return nil
			}
			if err := migration.Apply(executor); err != nil {
				return fmt.Errorf("apply schema migration %d (%s): %w", migration.Version, migration.Name, err)
			}
			if _, err := executor.Exec(
				"INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)",
				migration.Version, migration.Name, time.Now().UTC(),
			); err != nil {
				return fmt.Errorf("record schema migration %d: %w", migration.Version, err)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("check schema foreign keys: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID interface{}
		var parent string
		var constraint int
		if err := rows.Scan(&table, &rowID, &parent, &constraint); err != nil {
			return fmt.Errorf("read foreign key violation: %w", err)
		}
		return fmt.Errorf("foreign key violation in %s row %v referencing %s", table, rowID, parent)
	}
	return rows.Err()
}

func runImmediateSchemaTransaction(connection *sql.Conn, operation func(schemaMigrationExecutor) error) (returnErr error) {
	if _, err := connection.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin immediate schema transaction: %w", err)
	}
	defer func() {
		if returnErr != nil {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	if err := operation(dedicatedSchemaMigrationExecutor{connection: connection}); err != nil {
		return err
	}
	if _, err := connection.ExecContext(context.Background(), "COMMIT"); err != nil {
		return fmt.Errorf("commit schema transaction: %w", err)
	}
	return nil
}

func migrateLegacySchemaBaseline(tx schemaMigrationExecutor) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS configuration (key TEXT PRIMARY KEY, value TEXT NOT NULL, description TEXT, updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS printer_configs (printer_id TEXT PRIMARY KEY, name TEXT NOT NULL, model TEXT, ip_address TEXT NOT NULL, api_key TEXT, prusalink_username TEXT NOT NULL DEFAULT '', prusalink_password TEXT NOT NULL DEFAULT '', prusalink_custom_ca_pem TEXT NOT NULL DEFAULT '', toolheads INTEGER DEFAULT 1, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS toolhead_mappings (printer_name TEXT, toolhead_id INTEGER, spool_id INTEGER, mapped_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (printer_name, toolhead_id))`,
		`CREATE TABLE IF NOT EXISTS print_history (id INTEGER PRIMARY KEY AUTOINCREMENT, printer_name TEXT, toolhead_id INTEGER, spool_id INTEGER, filament_used REAL, print_started TIMESTAMP, print_finished TIMESTAMP, job_name TEXT, source_path TEXT, import_source TEXT NOT NULL DEFAULT 'runtime', external_job_id TEXT, external_lifetime_id TEXT, external_printer_uuid TEXT, print_state TEXT, accounting_key TEXT)`,
		`CREATE TABLE IF NOT EXISTS printer_job_checkpoints (printer_id TEXT PRIMARY KEY, printer_name TEXT NOT NULL, prusalink_job_id INTEGER, source_path TEXT NOT NULL, job_name TEXT NOT NULL, filament_usage_json TEXT NOT NULL DEFAULT '{}', tool_assignments_json TEXT NOT NULL DEFAULT '{}', last_state TEXT NOT NULL, progress REAL NOT NULL DEFAULT 0, started_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL, accounting_status TEXT NOT NULL DEFAULT 'pending', terminal_state TEXT, accounting_owner TEXT, accounting_lease_until INTEGER)`,
		`CREATE TABLE IF NOT EXISTS printer_job_adjustments (printer_id TEXT NOT NULL, checkpoint_started_at TIMESTAMP NOT NULL, logical_tool_id INTEGER NOT NULL, physical_toolhead_id INTEGER NOT NULL, spool_id INTEGER, filament_used REAL NOT NULL, authority TEXT NOT NULL, used_weight_before REAL, used_weight_after REAL, status TEXT NOT NULL DEFAULT 'pending', last_error TEXT, updated_at TIMESTAMP NOT NULL, PRIMARY KEY (printer_id, checkpoint_started_at, logical_tool_id))`,
		`CREATE TABLE IF NOT EXISTS nfc_sessions (session_id TEXT PRIMARY KEY, spool_id INTEGER, printer_name TEXT, toolhead_id INTEGER, location_name TEXT, is_printer_location BOOLEAN, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, expires_at TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS toolhead_names (printer_id TEXT, toolhead_id INTEGER, display_name TEXT NOT NULL, PRIMARY KEY (printer_id, toolhead_id))`,
		`CREATE TABLE IF NOT EXISTS logical_tool_routes (printer_id TEXT NOT NULL, logical_tool_id INTEGER NOT NULL, physical_toolhead_id INTEGER NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (printer_id, logical_tool_id))`,
		`CREATE TABLE IF NOT EXISTS spool_consumption_authorities (spool_id INTEGER PRIMARY KEY, authority TEXT NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("create legacy schema baseline: %w", err)
		}
	}
	columns := []struct {
		table  string
		column string
		query  string
	}{
		{"printer_configs", "prusalink_username", `ALTER TABLE printer_configs ADD COLUMN prusalink_username TEXT NOT NULL DEFAULT ''`},
		{"printer_configs", "prusalink_password", `ALTER TABLE printer_configs ADD COLUMN prusalink_password TEXT NOT NULL DEFAULT ''`},
		{"printer_configs", "prusalink_custom_ca_pem", `ALTER TABLE printer_configs ADD COLUMN prusalink_custom_ca_pem TEXT NOT NULL DEFAULT ''`},
		{"printer_job_checkpoints", "accounting_owner", `ALTER TABLE printer_job_checkpoints ADD COLUMN accounting_owner TEXT`},
		{"printer_job_checkpoints", "accounting_lease_until", `ALTER TABLE printer_job_checkpoints ADD COLUMN accounting_lease_until INTEGER`},
		{"print_history", "source_path", `ALTER TABLE print_history ADD COLUMN source_path TEXT`},
		{"print_history", "import_source", `ALTER TABLE print_history ADD COLUMN import_source TEXT NOT NULL DEFAULT 'runtime'`},
		{"print_history", "external_job_id", `ALTER TABLE print_history ADD COLUMN external_job_id TEXT`},
		{"print_history", "external_lifetime_id", `ALTER TABLE print_history ADD COLUMN external_lifetime_id TEXT`},
		{"print_history", "external_printer_uuid", `ALTER TABLE print_history ADD COLUMN external_printer_uuid TEXT`},
		{"print_history", "print_state", `ALTER TABLE print_history ADD COLUMN print_state TEXT`},
		{"print_history", "accounting_key", `ALTER TABLE print_history ADD COLUMN accounting_key TEXT`},
	}
	known := make(map[string]map[string]bool)
	for _, column := range columns {
		if known[column.table] == nil {
			tableColumns, err := tableColumnsTx(tx, column.table)
			if err != nil {
				return err
			}
			known[column.table] = tableColumns
		}
		if known[column.table][column.column] {
			continue
		}
		if _, err := tx.Exec(column.query); err != nil {
			return fmt.Errorf("add %s.%s: %w", column.table, column.column, err)
		}
		known[column.table][column.column] = true
	}
	return nil
}

func migrateStablePrinterIdentity(tx schemaMigrationExecutor) error {
	mappingColumns, err := tableColumnsTx(tx, "toolhead_mappings")
	if err != nil {
		return err
	}
	if !mappingColumns["printer_id"] {
		if err := validateLegacyPrinterNames(tx, "toolhead_mappings", "printer_name", "1 = 1"); err != nil {
			return err
		}
		if _, err := tx.Exec("ALTER TABLE toolhead_mappings RENAME TO toolhead_mappings_legacy_identity"); err != nil {
			return fmt.Errorf("rename legacy toolhead mappings: %w", err)
		}
		if _, err := tx.Exec(`CREATE TABLE toolhead_mappings (
			printer_id TEXT NOT NULL REFERENCES printer_configs(printer_id) ON UPDATE CASCADE ON DELETE CASCADE,
			toolhead_id INTEGER NOT NULL CHECK (toolhead_id >= 0),
			spool_id INTEGER NOT NULL CHECK (spool_id > 0),
			mapped_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (printer_id, toolhead_id),
			UNIQUE (spool_id)
		)`); err != nil {
			return fmt.Errorf("create stable toolhead mappings: %w", err)
		}
		if _, err := tx.Exec(`INSERT INTO toolhead_mappings (printer_id, toolhead_id, spool_id, mapped_at)
			SELECT p.printer_id, m.toolhead_id, m.spool_id, COALESCE(m.mapped_at, CURRENT_TIMESTAMP)
			FROM toolhead_mappings_legacy_identity m
			JOIN printer_configs p ON p.name = m.printer_name`); err != nil {
			return fmt.Errorf("migrate toolhead mappings: %w", err)
		}
		if _, err := tx.Exec("DROP TABLE toolhead_mappings_legacy_identity"); err != nil {
			return fmt.Errorf("drop legacy toolhead mappings: %w", err)
		}
	}

	if err := migrateNFCSessionsIdentity(tx); err != nil {
		return err
	}
	if err := migratePrintHistoryIdentity(tx); err != nil {
		return err
	}
	if err := addPrinterForeignKeys(tx); err != nil {
		return err
	}
	return nil
}

func migratePrinterAccountingForeignKeys(tx schemaMigrationExecutor) error {
	if err := rebuildPrinterJobCheckpoints(tx); err != nil {
		return err
	}
	return rebuildPrinterJobAdjustments(tx)
}

func rebuildPrinterJobCheckpoints(tx schemaMigrationExecutor) error {
	foreign, err := tableHasForeignKeyTx(tx, "printer_job_checkpoints", "printer_configs")
	if err != nil || foreign {
		return err
	}
	if err := validateStablePrinterIDs(tx, "printer_job_checkpoints"); err != nil {
		return err
	}
	if _, err := tx.Exec("ALTER TABLE printer_job_checkpoints RENAME TO printer_job_checkpoints_legacy_identity"); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE printer_job_checkpoints (
		printer_id TEXT PRIMARY KEY REFERENCES printer_configs(printer_id) ON UPDATE CASCADE ON DELETE CASCADE,
		printer_name TEXT NOT NULL,
		prusalink_job_id INTEGER,
		source_path TEXT NOT NULL,
		job_name TEXT NOT NULL,
		filament_usage_json TEXT NOT NULL DEFAULT '{}',
		tool_assignments_json TEXT NOT NULL DEFAULT '{}',
		last_state TEXT NOT NULL,
		progress REAL NOT NULL DEFAULT 0 CHECK (progress >= 0 AND progress <= 100),
		started_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		accounting_status TEXT NOT NULL DEFAULT 'pending',
		terminal_state TEXT,
		accounting_owner TEXT,
		accounting_lease_until INTEGER
	)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO printer_job_checkpoints SELECT * FROM printer_job_checkpoints_legacy_identity`); err != nil {
		return fmt.Errorf("migrate printer job checkpoints: %w", err)
	}
	_, err = tx.Exec("DROP TABLE printer_job_checkpoints_legacy_identity")
	return err
}

func rebuildPrinterJobAdjustments(tx schemaMigrationExecutor) error {
	foreign, err := tableHasForeignKeyTx(tx, "printer_job_adjustments", "printer_configs")
	if err != nil || foreign {
		return err
	}
	if err := validateStablePrinterIDs(tx, "printer_job_adjustments"); err != nil {
		return err
	}
	if _, err := tx.Exec("ALTER TABLE printer_job_adjustments RENAME TO printer_job_adjustments_legacy_identity"); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE printer_job_adjustments (
		printer_id TEXT NOT NULL REFERENCES printer_configs(printer_id) ON UPDATE CASCADE ON DELETE CASCADE,
		checkpoint_started_at TIMESTAMP NOT NULL,
		logical_tool_id INTEGER NOT NULL CHECK (logical_tool_id >= 0),
		physical_toolhead_id INTEGER NOT NULL CHECK (physical_toolhead_id >= 0),
		spool_id INTEGER,
		filament_used REAL NOT NULL CHECK (filament_used >= 0),
		authority TEXT NOT NULL,
		used_weight_before REAL,
		used_weight_after REAL,
		status TEXT NOT NULL DEFAULT 'pending',
		last_error TEXT,
		updated_at TIMESTAMP NOT NULL,
		PRIMARY KEY (printer_id, checkpoint_started_at, logical_tool_id)
	)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO printer_job_adjustments SELECT * FROM printer_job_adjustments_legacy_identity`); err != nil {
		return fmt.Errorf("migrate printer job adjustments: %w", err)
	}
	_, err = tx.Exec("DROP TABLE printer_job_adjustments_legacy_identity")
	return err
}

func validateStablePrinterIDs(tx schemaMigrationExecutor, table string) error {
	query := fmt.Sprintf(`SELECT DISTINCT r.printer_id FROM %s r
		LEFT JOIN printer_configs p ON p.printer_id = r.printer_id
		WHERE p.printer_id IS NULL ORDER BY r.printer_id LIMIT 1`, table)
	var printerID string
	err := tx.QueryRow(query).Scan(&printerID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("validate printer IDs in %s: %w", table, err)
	}
	return fmt.Errorf("printer ID %q in %s has no matching printer", printerID, table)
}

func validateLegacyPrinterNames(tx schemaMigrationExecutor, table string, column string, condition string) error {
	query := fmt.Sprintf(`SELECT DISTINCT %s FROM %s WHERE %s AND %s IS NOT NULL AND TRIM(%s) != '' ORDER BY %s`, column, table, condition, column, column, column)
	rows, err := tx.Query(query)
	if err != nil {
		return fmt.Errorf("inspect legacy printer names in %s: %w", table, err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, name := range names {
		var count int
		if err := tx.QueryRow("SELECT COUNT(*) FROM printer_configs WHERE name = ?", name).Scan(&count); err != nil {
			return err
		}
		switch {
		case count == 0:
			return fmt.Errorf("legacy printer name %q in %s has no matching printer", name, table)
		case count > 1:
			return fmt.Errorf("ambiguous legacy printer name %q in %s matches %d printers", name, table, count)
		}
	}
	return nil
}

func migrateNFCSessionsIdentity(tx schemaMigrationExecutor) error {
	columns, err := tableColumnsTx(tx, "nfc_sessions")
	if err != nil || columns["printer_id"] {
		return err
	}
	if err := validateLegacyPrinterNames(tx, "nfc_sessions", "printer_name", "is_printer_location = 1"); err != nil {
		return err
	}
	if _, err := tx.Exec("ALTER TABLE nfc_sessions RENAME TO nfc_sessions_legacy_identity"); err != nil {
		return fmt.Errorf("rename legacy NFC sessions: %w", err)
	}
	if _, err := tx.Exec(`CREATE TABLE nfc_sessions (
		session_id TEXT PRIMARY KEY,
		spool_id INTEGER NOT NULL DEFAULT 0,
		printer_id TEXT REFERENCES printer_configs(printer_id) ON UPDATE CASCADE ON DELETE CASCADE,
		toolhead_id INTEGER NOT NULL DEFAULT 0 CHECK (toolhead_id >= 0),
		location_name TEXT NOT NULL DEFAULT '',
		is_printer_location BOOLEAN NOT NULL DEFAULT 0,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		expires_at TIMESTAMP NOT NULL
	)`); err != nil {
		return fmt.Errorf("create stable NFC sessions: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO nfc_sessions (session_id, spool_id, printer_id, toolhead_id, location_name, is_printer_location, created_at, expires_at)
		SELECT n.session_id, COALESCE(n.spool_id, 0),
			CASE WHEN n.is_printer_location = 1 THEN p.printer_id ELSE NULL END,
			COALESCE(n.toolhead_id, 0), COALESCE(n.location_name, ''), COALESCE(n.is_printer_location, 0),
			COALESCE(n.created_at, CURRENT_TIMESTAMP), COALESCE(n.expires_at, CURRENT_TIMESTAMP)
		FROM nfc_sessions_legacy_identity n
		LEFT JOIN printer_configs p ON n.is_printer_location = 1 AND p.name = n.printer_name`); err != nil {
		return fmt.Errorf("migrate NFC sessions: %w", err)
	}
	_, err = tx.Exec("DROP TABLE nfc_sessions_legacy_identity")
	return err
}

func migratePrintHistoryIdentity(tx schemaMigrationExecutor) error {
	columns, err := tableColumnsTx(tx, "print_history")
	if err != nil || (columns["printer_id"] && columns["printer_name_at_event"]) {
		return err
	}
	if _, err := tx.Exec("ALTER TABLE print_history RENAME TO print_history_legacy_identity"); err != nil {
		return fmt.Errorf("rename legacy print history: %w", err)
	}
	if _, err := tx.Exec(`CREATE TABLE print_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		printer_id TEXT REFERENCES printer_configs(printer_id) ON UPDATE CASCADE ON DELETE SET NULL,
		printer_name_at_event TEXT NOT NULL,
		toolhead_id INTEGER NOT NULL CHECK (toolhead_id >= 0),
		spool_id INTEGER,
		filament_used REAL NOT NULL CHECK (filament_used >= 0),
		print_started TIMESTAMP NOT NULL,
		print_finished TIMESTAMP NOT NULL,
		job_name TEXT NOT NULL,
		source_path TEXT,
		import_source TEXT NOT NULL DEFAULT 'runtime',
		external_job_id TEXT,
		external_lifetime_id TEXT,
		external_printer_uuid TEXT,
		print_state TEXT,
		accounting_key TEXT
	)`); err != nil {
		return fmt.Errorf("create stable print history: %w", err)
	}

	// The legacy bootstrap has normalized optional columns before this migration.
	if _, err := tx.Exec(`INSERT INTO print_history (
		id, printer_id, printer_name_at_event, toolhead_id, spool_id, filament_used,
		print_started, print_finished, job_name, source_path, import_source,
		external_job_id, external_lifetime_id, external_printer_uuid, print_state, accounting_key
	)
	SELECT h.id,
		CASE WHEN (SELECT COUNT(*) FROM printer_configs p2 WHERE p2.name = h.printer_name) = 1
			THEN (SELECT p3.printer_id FROM printer_configs p3 WHERE p3.name = h.printer_name LIMIT 1)
			ELSE NULL END,
		COALESCE(NULLIF(TRIM(h.printer_name), ''), 'Unknown printer'),
		COALESCE(h.toolhead_id, 0), h.spool_id, COALESCE(h.filament_used, 0),
		COALESCE(h.print_started, h.print_finished, CURRENT_TIMESTAMP),
		COALESCE(h.print_finished, h.print_started, CURRENT_TIMESTAMP),
		COALESCE(h.job_name, ''), h.source_path, COALESCE(h.import_source, 'runtime'),
		h.external_job_id, NULLIF(TRIM(h.external_lifetime_id), ''), h.external_printer_uuid, h.print_state, h.accounting_key
	FROM print_history_legacy_identity h`); err != nil {
		return fmt.Errorf("migrate print history: %w", err)
	}
	if _, err := tx.Exec("DROP TABLE print_history_legacy_identity"); err != nil {
		return err
	}
	indexes := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_print_history_import_lifetime ON print_history(import_source, external_lifetime_id, toolhead_id) WHERE external_lifetime_id IS NOT NULL AND external_lifetime_id != ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_print_history_import_job ON print_history(import_source, external_printer_uuid, external_job_id, toolhead_id) WHERE external_lifetime_id IS NULL AND external_printer_uuid IS NOT NULL AND external_printer_uuid != '' AND external_job_id IS NOT NULL AND external_job_id != ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_print_history_accounting_key ON print_history(accounting_key) WHERE accounting_key IS NOT NULL AND accounting_key != ''`,
	}
	for _, statement := range indexes {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func normalizeImportedJobLifetimeIdentity(tx schemaMigrationExecutor) error {
	if _, err := tx.Exec(`UPDATE print_history
		SET external_lifetime_id = NULL
		WHERE external_lifetime_id IS NOT NULL AND TRIM(external_lifetime_id) = ''`); err != nil {
		return fmt.Errorf("normalize empty imported job lifetime IDs: %w", err)
	}
	return nil
}

func addPrinterForeignKeys(tx schemaMigrationExecutor) error {
	for _, table := range []string{"toolhead_names", "logical_tool_routes"} {
		if err := rebuildSimplePrinterRelation(tx, table); err != nil {
			return err
		}
	}
	return nil
}

func rebuildSimplePrinterRelation(tx schemaMigrationExecutor, table string) error {
	foreign, err := tableHasForeignKeyTx(tx, table, "printer_configs")
	if err != nil || foreign {
		return err
	}
	legacy := table + "_legacy_identity"
	if _, err := tx.Exec("ALTER TABLE " + table + " RENAME TO " + legacy); err != nil {
		return err
	}
	var create, copySQL string
	switch table {
	case "toolhead_names":
		create = `CREATE TABLE toolhead_names (printer_id TEXT NOT NULL REFERENCES printer_configs(printer_id) ON UPDATE CASCADE ON DELETE CASCADE, toolhead_id INTEGER NOT NULL CHECK (toolhead_id >= 0), display_name TEXT NOT NULL, PRIMARY KEY (printer_id, toolhead_id))`
		copySQL = `INSERT INTO toolhead_names SELECT printer_id, toolhead_id, display_name FROM toolhead_names_legacy_identity`
	case "logical_tool_routes":
		create = `CREATE TABLE logical_tool_routes (printer_id TEXT NOT NULL REFERENCES printer_configs(printer_id) ON UPDATE CASCADE ON DELETE CASCADE, logical_tool_id INTEGER NOT NULL CHECK (logical_tool_id >= 0), physical_toolhead_id INTEGER NOT NULL CHECK (physical_toolhead_id >= 0), updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (printer_id, logical_tool_id))`
		copySQL = `INSERT INTO logical_tool_routes SELECT printer_id, logical_tool_id, physical_toolhead_id, updated_at FROM logical_tool_routes_legacy_identity`
	default:
		return fmt.Errorf("unsupported printer relation %q", table)
	}
	if _, err := tx.Exec(create); err != nil {
		return err
	}
	if _, err := tx.Exec(copySQL); err != nil {
		return fmt.Errorf("migrate %s: %w", table, err)
	}
	_, err = tx.Exec("DROP TABLE " + legacy)
	return err
}

func tableColumnsTx(tx schemaMigrationExecutor, table string) (map[string]bool, error) {
	rows, err := tx.Query("PRAGMA table_info(" + table + ")")
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

func tableHasForeignKeyTx(tx schemaMigrationExecutor, table string, referencedTable string) (bool, error) {
	rows, err := tx.Query("PRAGMA foreign_key_list(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, seq int
		var target, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &target, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return false, err
		}
		if strings.EqualFold(target, referencedTable) {
			return true, nil
		}
	}
	return false, rows.Err()
}
