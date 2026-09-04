package main

import (
	"fmt"
	"log"
)

func (b *FilamentBridge) ensurePrinterProtocolSchema() error {
	rows, err := b.db.Query("PRAGMA table_info(printer_configs)")
	if err != nil {
		return fmt.Errorf("inspect printer config schema: %w", err)
	}
	existing := make(map[string]bool)
	for rows.Next() {
		var columnID int
		var name string
		var columnType string
		var notNull int
		var defaultValue interface{}
		var primaryKey int
		if err := rows.Scan(&columnID, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan printer config schema: %w", err)
		}
		existing[name] = true
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close printer config schema rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate printer config schema: %w", err)
	}

	columns := []struct {
		name  string
		query string
	}{
		{name: "prusalink_username", query: "ALTER TABLE printer_configs ADD COLUMN prusalink_username TEXT NOT NULL DEFAULT ''"},
		{name: "prusalink_password", query: "ALTER TABLE printer_configs ADD COLUMN prusalink_password TEXT NOT NULL DEFAULT ''"},
		{name: "prusalink_custom_ca_pem", query: "ALTER TABLE printer_configs ADD COLUMN prusalink_custom_ca_pem TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range columns {
		if existing[column.name] {
			continue
		}
		if _, err := b.db.Exec(column.query); err != nil {
			return fmt.Errorf("add printer config column %s: %w", column.name, err)
		}
	}
	return nil
}

func newConfiguredPrusaLinkClient(config PrinterConfig, timeout int, fileDownloadTimeout int) (*PrusaLinkClient, error) {
	return NewPrusaLinkClientWithOptions(PrusaLinkClientOptions{
		BaseURL:             config.IPAddress,
		APIKey:              config.APIKey,
		DigestUsername:      config.PrusaLinkUsername,
		DigestPassword:      config.PrusaLinkPassword,
		CustomCAPEM:         []byte(config.PrusaLinkCustomCAPEM),
		Timeout:             timeout,
		FileDownloadTimeout: fileDownloadTimeout,
	})
}

func (b *FilamentBridge) logPrusaLinkDiagnosticsOnce(printerID string, client *PrusaLinkClient) {
	b.mutex.Lock()
	if b.diagnosticsLogged[printerID] {
		b.mutex.Unlock()
		return
	}
	b.diagnosticsLogged[printerID] = true
	b.mutex.Unlock()

	diagnostics, err := client.GetDiagnostics()
	if err != nil {
		log.Printf("PrusaLink diagnostics unavailable for %s: %v", printerID, err)
		return
	}
	log.Printf("PrusaLink %s: API=%s server=%s firmware=%s printer=%s HTTPS=%t capabilities=%+v",
		printerID, diagnostics.API, diagnostics.Server, diagnostics.Firmware,
		diagnostics.Printer, diagnostics.HTTPS, diagnostics.Capabilities)
}
