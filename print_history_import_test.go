package main

import (
	"testing"
	"time"
)

func TestPrusaConnectJobNameUsesBasenameFallback(t *testing.T) {
	job := prusaConnectJob{
		Path: "usb/MERGED~1.BGC",
	}

	if got := job.JobName(); got != "MERGED~1.BGC" {
		t.Fatalf("JobName() = %q, want %q", got, "MERGED~1.BGC")
	}
}

func TestImportPrusaConnectPrintHistoryImportsRows(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()

	bridge := newTestBridge(t, spoolman.server.URL)

	if err := bridge.SavePrinterConfig("printer-a", PrinterConfig{
		Name:      "Printer A",
		Model:     ModelCoreOne,
		IPAddress: "printer-a.local",
		Toolheads: 1,
	}); err != nil {
		t.Fatalf("SavePrinterConfig() error = %v", err)
	}

	payload := `{
		"jobs": [
			{
				"id": 70,
				"lifetime_id": "job-70",
				"printer_uuid": "uuid-printer-a",
				"state": "FIN_OK",
				"start": 1713700000,
				"end": 1713703600,
				"file": {
					"display_name": "cube.bgcode",
					"meta": {
						"filament_used_g": 59.39
					}
				}
			},
			{
				"id": 71,
				"lifetime_id": "job-71",
				"printer_uuid": "uuid-printer-a",
				"state": "RUNNING",
				"start": 1713700000,
				"end": 0,
				"file": {
					"display_name": "running.bgcode",
					"meta": {
						"filament_used_g": 20.00
					}
				}
			},
			{
				"id": 72,
				"lifetime_id": "job-72",
				"printer_uuid": "uuid-printer-a",
				"state": "FIN_OK",
				"start": 1713700000,
				"end": 1713707200,
				"file": {
					"display_name": "empty.bgcode",
					"meta": {}
				}
			}
		]
	}`

	summary, err := bridge.ImportPrusaConnectPrintHistory("printer-a", 0, []byte(payload))
	if err != nil {
		t.Fatalf("ImportPrusaConnectPrintHistory() error = %v", err)
	}

	if summary.JobsSeen != 3 {
		t.Fatalf("JobsSeen = %d, want 3", summary.JobsSeen)
	}
	if summary.ImportedRows != 1 {
		t.Fatalf("ImportedRows = %d, want 1", summary.ImportedRows)
	}
	if summary.SkippedJobs != 2 {
		t.Fatalf("SkippedJobs = %d, want 2", summary.SkippedJobs)
	}

	history, err := bridge.GetPrintHistory(10)
	if err != nil {
		t.Fatalf("GetPrintHistory() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history length = %d, want 1", len(history))
	}
	if history[0].PrinterName != "Printer A" {
		t.Fatalf("history printer = %q, want %q", history[0].PrinterName, "Printer A")
	}
	if history[0].SpoolID != nil {
		t.Fatalf("history spool id = %v, want nil", history[0].SpoolID)
	}
	if history[0].ToolheadID != 0 {
		t.Fatalf("history toolhead id = %d, want 0", history[0].ToolheadID)
	}
	if history[0].JobName != "cube.bgcode" {
		t.Fatalf("history job name = %q, want %q", history[0].JobName, "cube.bgcode")
	}
	if history[0].FilamentUsed != 59.39 {
		t.Fatalf("history filament used = %.2f, want 59.39", history[0].FilamentUsed)
	}
	if history[0].PrintStarted.Unix() != 1713700000 {
		t.Fatalf("history print started = %d, want 1713700000", history[0].PrintStarted.Unix())
	}
	if history[0].PrintFinished.Unix() != 1713703600 {
		t.Fatalf("history print finished = %d, want 1713703600", history[0].PrintFinished.Unix())
	}
}

func TestImportPrusaConnectPrintHistorySkipsDuplicateRows(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()

	bridge := newTestBridge(t, spoolman.server.URL)

	if err := bridge.SavePrinterConfig("printer-a", PrinterConfig{
		Name:      "Printer A",
		Model:     ModelCoreOne,
		IPAddress: "printer-a.local",
		Toolheads: 1,
	}); err != nil {
		t.Fatalf("SavePrinterConfig() error = %v", err)
	}

	payload := `{
		"jobs": [
			{
				"id": 70,
				"lifetime_id": "job-70",
				"printer_uuid": "uuid-printer-a",
				"state": "FIN_OK",
				"start": 1713700000,
				"end": 1713703600,
				"file": {
					"display_name": "cube.bgcode",
					"meta": {
						"filament_used_g": 59.39
					}
				}
			}
		]
	}`

	if _, err := bridge.ImportPrusaConnectPrintHistory("printer-a", 0, []byte(payload)); err != nil {
		t.Fatalf("first ImportPrusaConnectPrintHistory() error = %v", err)
	}

	summary, err := bridge.ImportPrusaConnectPrintHistory("printer-a", 0, []byte(payload))
	if err != nil {
		t.Fatalf("second ImportPrusaConnectPrintHistory() error = %v", err)
	}

	if summary.ImportedRows != 0 {
		t.Fatalf("ImportedRows = %d, want 0", summary.ImportedRows)
	}
	if summary.DuplicateRows != 1 {
		t.Fatalf("DuplicateRows = %d, want 1", summary.DuplicateRows)
	}

	history, err := bridge.GetPrintHistory(10)
	if err != nil {
		t.Fatalf("GetPrintHistory() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history length = %d, want 1", len(history))
	}
}

func TestImportPrusaConnectPrintHistoryUsesPerToolUsage(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()

	bridge := newTestBridge(t, spoolman.server.URL)

	if err := bridge.SavePrinterConfig("printer-xl", PrinterConfig{
		Name:      "Printer XL",
		Model:     ModelXL,
		IPAddress: "printer-xl.local",
		Toolheads: 2,
	}); err != nil {
		t.Fatalf("SavePrinterConfig() error = %v", err)
	}

	payload := `{
		"jobs": [
			{
				"id": 99,
				"lifetime_id": "job-99",
				"printer_uuid": "uuid-printer-xl",
				"state": "FIN_OK",
				"start": 1713800000,
				"end": 1713807200,
				"file": {
					"display_name": "multitool.bgcode",
					"meta": {
						"filament_used_g_per_tool": [12.5, 7.25]
					}
				}
			}
		]
	}`

	summary, err := bridge.ImportPrusaConnectPrintHistory("printer-xl", 0, []byte(payload))
	if err != nil {
		t.Fatalf("ImportPrusaConnectPrintHistory() error = %v", err)
	}

	if summary.ImportedRows != 2 {
		t.Fatalf("ImportedRows = %d, want 2", summary.ImportedRows)
	}

	history, err := bridge.GetPrintHistory(10)
	if err != nil {
		t.Fatalf("GetPrintHistory() error = %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history length = %d, want 2", len(history))
	}

	usageByToolhead := make(map[int]float64)
	for _, entry := range history {
		usageByToolhead[entry.ToolheadID] = entry.FilamentUsed
	}

	if usageByToolhead[0] != 12.5 {
		t.Fatalf("toolhead 0 usage = %.2f, want 12.50", usageByToolhead[0])
	}
	if usageByToolhead[1] != 7.25 {
		t.Fatalf("toolhead 1 usage = %.2f, want 7.25", usageByToolhead[1])
	}
}

func TestImportPrusaConnectPrintHistoryImportsOldestFinishedFirst(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()

	bridge := newTestBridge(t, spoolman.server.URL)

	if err := bridge.SavePrinterConfig("printer-a", PrinterConfig{
		Name:      "Printer A",
		Model:     ModelCoreOne,
		IPAddress: "printer-a.local",
		Toolheads: 1,
	}); err != nil {
		t.Fatalf("SavePrinterConfig() error = %v", err)
	}

	payload := `{
		"jobs": [
			{
				"id": 200,
				"lifetime_id": "job-200",
				"printer_uuid": "uuid-printer-a",
				"state": "FIN_OK",
				"start": 1713803600,
				"end": 1713807200,
				"file": {
					"display_name": "newer.bgcode",
					"meta": {
						"filament_used_g": 10.5
					}
				}
			},
			{
				"id": 100,
				"lifetime_id": "job-100",
				"printer_uuid": "uuid-printer-a",
				"state": "FIN_OK",
				"start": 1713700000,
				"end": 1713703600,
				"file": {
					"display_name": "older.bgcode",
					"meta": {
						"filament_used_g": 9.25
					}
				}
			}
		]
	}`

	if _, err := bridge.ImportPrusaConnectPrintHistory("printer-a", 0, []byte(payload)); err != nil {
		t.Fatalf("ImportPrusaConnectPrintHistory() error = %v", err)
	}

	rows, err := bridge.db.Query(`
		SELECT job_name, print_finished
		FROM print_history
		ORDER BY id ASC
	`)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	defer rows.Close()

	var jobNames []string
	var finished []time.Time
	for rows.Next() {
		var jobName string
		var printFinished time.Time
		if err := rows.Scan(&jobName, &printFinished); err != nil {
			t.Fatalf("Scan() error = %v", err)
		}
		jobNames = append(jobNames, jobName)
		finished = append(finished, printFinished)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err() = %v", err)
	}

	if len(jobNames) != 2 {
		t.Fatalf("stored rows = %d, want 2", len(jobNames))
	}
	if jobNames[0] != "older.bgcode" || jobNames[1] != "newer.bgcode" {
		t.Fatalf("import order = %v, want [older.bgcode newer.bgcode]", jobNames)
	}
	if !finished[0].Before(finished[1]) {
		t.Fatalf("finish order = [%d %d], want oldest first", finished[0].Unix(), finished[1].Unix())
	}
}

func TestImportPrusaConnectPrintHistoryIsIdempotentAcrossIdentifierChanges(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()

	bridge := newTestBridge(t, spoolman.server.URL)

	if err := bridge.SavePrinterConfig("printer-a", PrinterConfig{
		Name:      "Printer A",
		Model:     ModelCoreOne,
		IPAddress: "printer-a.local",
		Toolheads: 1,
	}); err != nil {
		t.Fatalf("SavePrinterConfig() error = %v", err)
	}

	firstPayload := `{
		"jobs": [
			{
				"id": 70,
				"lifetime_id": "job-70",
				"printer_uuid": "uuid-printer-a",
				"state": "FIN_OK",
				"start": 1713700000,
				"end": 1713703600,
				"file": {
					"display_name": "cube.bgcode",
					"meta": {
						"filament_used_g": 59.39
					}
				}
			}
		]
	}`
	secondPayload := `{
		"jobs": [
			{
				"id": 170,
				"lifetime_id": "job-170",
				"printer_uuid": "uuid-printer-b",
				"state": "FIN_OK",
				"start": 1713700000,
				"end": 1713703600,
				"file": {
					"display_name": "cube.bgcode",
					"meta": {
						"filament_used_g": 59.39
					}
				}
			}
		]
	}`

	if _, err := bridge.ImportPrusaConnectPrintHistory("printer-a", 0, []byte(firstPayload)); err != nil {
		t.Fatalf("first ImportPrusaConnectPrintHistory() error = %v", err)
	}

	summary, err := bridge.ImportPrusaConnectPrintHistory("printer-a", 0, []byte(secondPayload))
	if err != nil {
		t.Fatalf("second ImportPrusaConnectPrintHistory() error = %v", err)
	}

	if summary.ImportedRows != 0 {
		t.Fatalf("ImportedRows = %d, want 0", summary.ImportedRows)
	}
	if summary.DuplicateRows != 1 {
		t.Fatalf("DuplicateRows = %d, want 1", summary.DuplicateRows)
	}

	history, err := bridge.GetPrintHistory(10)
	if err != nil {
		t.Fatalf("GetPrintHistory() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history length = %d, want 1", len(history))
	}
}
