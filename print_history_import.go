package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	printHistoryImportSourceRuntime      = "runtime"
	printHistoryImportSourcePrusaConnect = "prusa_connect"
)

type printHistoryImportValidationError struct {
	message string
}

func (e *printHistoryImportValidationError) Error() string {
	return e.message
}

type PrintHistoryImportSummary struct {
	JobsSeen      int `json:"jobs_seen"`
	ImportedRows  int `json:"imported_rows"`
	DuplicateRows int `json:"duplicate_rows"`
	SkippedJobs   int `json:"skipped_jobs"`
	SkippedRows   int `json:"skipped_rows"`
}

type prusaConnectJobsEnvelope struct {
	Jobs []prusaConnectJob `json:"jobs"`
}

type prusaConnectJob struct {
	ID          json.Number         `json:"id"`
	LifetimeID  string              `json:"lifetime_id"`
	PrinterUUID string              `json:"printer_uuid"`
	State       string              `json:"state"`
	Path        string              `json:"path"`
	Start       int64               `json:"start"`
	End         int64               `json:"end"`
	File        prusaConnectJobFile `json:"file"`
}

type prusaConnectJobFile struct {
	Name        string               `json:"name"`
	DisplayName string               `json:"display_name"`
	Meta        prusaConnectFileMeta `json:"meta"`
}

type prusaConnectFileMeta struct {
	FilamentUsedG              float64   `json:"filament_used_g"`
	LegacyFilamentUsedG        float64   `json:"filament used [g]"`
	FilamentUsedGPerTool       []float64 `json:"filament_used_g_per_tool"`
	LegacyFilamentUsedGPerTool []float64 `json:"filament used [g] per tool"`
}

func (m prusaConnectFileMeta) FilamentUsageByToolhead(defaultToolheadID int) map[int]float64 {
	perTool := m.FilamentUsedGPerTool
	if len(perTool) == 0 {
		perTool = m.LegacyFilamentUsedGPerTool
	}

	if len(perTool) > 0 {
		usage := make(map[int]float64, len(perTool))
		for toolheadID, weight := range perTool {
			if weight <= 0 {
				continue
			}
			usage[toolheadID] = weight
		}
		if len(usage) > 0 {
			return usage
		}
	}

	totalWeight := m.FilamentUsedG
	if totalWeight <= 0 {
		totalWeight = m.LegacyFilamentUsedG
	}
	if totalWeight <= 0 {
		return nil
	}

	return map[int]float64{defaultToolheadID: totalWeight}
}

func (j prusaConnectJob) ExternalJobID() string {
	return strings.TrimSpace(j.ID.String())
}

func (j prusaConnectJob) JobName() string {
	switch {
	case strings.TrimSpace(j.File.DisplayName) != "":
		return strings.TrimSpace(j.File.DisplayName)
	case strings.TrimSpace(j.File.Name) != "":
		return prusaPathBase(j.File.Name)
	case strings.TrimSpace(j.Path) != "":
		return prusaPathBase(j.Path)
	case j.ExternalJobID() != "":
		return fmt.Sprintf("Prusa Connect Job %s", j.ExternalJobID())
	default:
		return "Imported Prusa Connect Job"
	}
}

func (j prusaConnectJob) HasRequiredTimes() bool {
	return j.Start > 0 && j.End > 0 && j.End >= j.Start
}

func (b *FilamentBridge) ensurePrintHistoryImportSchema() error {
	columns, err := b.getTableColumns("print_history")
	if err != nil {
		return err
	}

	alterStatements := []struct {
		column string
		query  string
	}{
		{
			column: "import_source",
			query:  "ALTER TABLE print_history ADD COLUMN import_source TEXT NOT NULL DEFAULT 'runtime'",
		},
		{
			column: "external_job_id",
			query:  "ALTER TABLE print_history ADD COLUMN external_job_id TEXT",
		},
		{
			column: "external_lifetime_id",
			query:  "ALTER TABLE print_history ADD COLUMN external_lifetime_id TEXT",
		},
		{
			column: "external_printer_uuid",
			query:  "ALTER TABLE print_history ADD COLUMN external_printer_uuid TEXT",
		},
		{
			column: "print_state",
			query:  "ALTER TABLE print_history ADD COLUMN print_state TEXT",
		},
	}

	for _, statement := range alterStatements {
		if columns[statement.column] {
			continue
		}
		if _, err := b.db.Exec(statement.query); err != nil {
			return fmt.Errorf("failed to add print_history.%s: %w", statement.column, err)
		}
	}

	indexStatements := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_print_history_import_lifetime
		ON print_history (import_source, external_lifetime_id, toolhead_id)
		WHERE external_lifetime_id IS NOT NULL AND external_lifetime_id != ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_print_history_import_job
		ON print_history (import_source, external_printer_uuid, external_job_id, toolhead_id)
		WHERE external_printer_uuid IS NOT NULL AND external_printer_uuid != '' AND external_job_id IS NOT NULL AND external_job_id != ''`,
	}

	for _, statement := range indexStatements {
		if _, err := b.db.Exec(statement); err != nil {
			return fmt.Errorf("failed to create print history import index: %w", err)
		}
	}

	return nil
}

func (b *FilamentBridge) getTableColumns(tableName string) (map[string]bool, error) {
	rows, err := b.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return nil, fmt.Errorf("failed to inspect table %s: %w", tableName, err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &primaryKey); err != nil {
			return nil, fmt.Errorf("failed to inspect table %s columns: %w", tableName, err)
		}
		columns[name] = true
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate table %s columns: %w", tableName, err)
	}

	return columns, nil
}

func parsePrusaConnectJobs(payload []byte) ([]prusaConnectJob, error) {
	trimmed := bytes.TrimSpace(payload)
	trimmed = bytes.TrimPrefix(trimmed, []byte{0xEF, 0xBB, 0xBF})
	if len(trimmed) == 0 {
		return nil, &printHistoryImportValidationError{message: "import payload is empty"}
	}

	switch trimmed[0] {
	case '{':
		var envelope prusaConnectJobsEnvelope
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.UseNumber()
		if err := decoder.Decode(&envelope); err == nil && envelope.Jobs != nil {
			return envelope.Jobs, nil
		}

		var job prusaConnectJob
		decoder = json.NewDecoder(bytes.NewReader(trimmed))
		decoder.UseNumber()
		if err := decoder.Decode(&job); err != nil {
			return nil, &printHistoryImportValidationError{message: "invalid Prusa Connect JSON payload"}
		}
		return []prusaConnectJob{job}, nil
	case '[':
		var jobs []prusaConnectJob
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.UseNumber()
		if err := decoder.Decode(&jobs); err != nil {
			return nil, &printHistoryImportValidationError{message: "invalid Prusa Connect JSON payload"}
		}
		return jobs, nil
	default:
		return nil, &printHistoryImportValidationError{message: "import payload must be JSON object or array"}
	}
}

func sortPrusaConnectJobsOldestFinishedFirst(jobs []prusaConnectJob) {
	sort.SliceStable(jobs, func(i, j int) bool {
		left := jobs[i]
		right := jobs[j]

		switch {
		case left.End != right.End:
			if left.End <= 0 {
				return false
			}
			if right.End <= 0 {
				return true
			}
			return left.End < right.End
		case left.Start != right.Start:
			if left.Start <= 0 {
				return false
			}
			if right.Start <= 0 {
				return true
			}
			return left.Start < right.Start
		case strings.TrimSpace(left.LifetimeID) != strings.TrimSpace(right.LifetimeID):
			return strings.TrimSpace(left.LifetimeID) < strings.TrimSpace(right.LifetimeID)
		case left.ExternalJobID() != right.ExternalJobID():
			return left.ExternalJobID() < right.ExternalJobID()
		case left.JobName() != right.JobName():
			return left.JobName() < right.JobName()
		default:
			return strings.TrimSpace(left.Path) < strings.TrimSpace(right.Path)
		}
	})
}

func (b *FilamentBridge) ImportPrusaConnectPrintHistory(printerID string, defaultToolheadID int, payload []byte) (*PrintHistoryImportSummary, error) {
	printerConfigs, err := b.GetAllPrinterConfigs()
	if err != nil {
		return nil, fmt.Errorf("failed to get printer configs: %w", err)
	}

	printerConfig, exists := printerConfigs[printerID]
	if !exists {
		return nil, &printHistoryImportValidationError{message: "selected printer not found"}
	}

	if defaultToolheadID < 0 || defaultToolheadID >= printerConfig.Toolheads {
		return nil, &printHistoryImportValidationError{message: fmt.Sprintf("default_toolhead_id must be between 0 and %d", printerConfig.Toolheads-1)}
	}

	jobs, err := parsePrusaConnectJobs(payload)
	if err != nil {
		return nil, err
	}
	sortPrusaConnectJobsOldestFinishedFirst(jobs)

	summary := &PrintHistoryImportSummary{
		JobsSeen: len(jobs),
	}

	tx, err := b.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to start import transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	for _, job := range jobs {
		if !job.HasRequiredTimes() {
			summary.SkippedJobs++
			continue
		}

		filamentUsage := job.File.Meta.FilamentUsageByToolhead(defaultToolheadID)
		if len(filamentUsage) == 0 {
			summary.SkippedJobs++
			continue
		}

		printStarted := time.Unix(job.Start, 0)
		printFinished := time.Unix(job.End, 0)
		jobName := job.JobName()

		importedAnyRow := false
		for toolheadID, filamentUsed := range filamentUsage {
			if filamentUsed <= 0 {
				summary.SkippedRows++
				continue
			}
			if toolheadID < 0 || toolheadID >= printerConfig.Toolheads {
				summary.SkippedRows++
				continue
			}

			exists, err := b.printHistoryFingerprintExistsTx(tx, printerConfig.Name, toolheadID, jobName, printStarted, printFinished)
			if err != nil {
				return nil, err
			}
			if exists {
				summary.DuplicateRows++
				continue
			}

			result, err := tx.Exec(`
				INSERT OR IGNORE INTO print_history (
					printer_name,
					toolhead_id,
					spool_id,
					filament_used,
					print_started,
					print_finished,
					job_name,
					import_source,
					external_job_id,
					external_lifetime_id,
					external_printer_uuid,
					print_state
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`,
				printerConfig.Name,
				toolheadID,
				nil,
				filamentUsed,
				printStarted,
				printFinished,
				jobName,
				printHistoryImportSourcePrusaConnect,
				job.ExternalJobID(),
				strings.TrimSpace(job.LifetimeID),
				strings.TrimSpace(job.PrinterUUID),
				strings.TrimSpace(job.State),
			)
			if err != nil {
				return nil, fmt.Errorf("failed to import job %s: %w", jobName, err)
			}

			rowsAffected, err := result.RowsAffected()
			if err != nil {
				return nil, fmt.Errorf("failed to inspect import result for job %s: %w", jobName, err)
			}

			if rowsAffected == 0 {
				summary.DuplicateRows++
				continue
			}

			summary.ImportedRows++
			importedAnyRow = true
		}

		if !importedAnyRow {
			hasPositiveUsage := false
			for _, filamentUsed := range filamentUsage {
				if filamentUsed > 0 {
					hasPositiveUsage = true
					break
				}
			}
			if !hasPositiveUsage {
				summary.SkippedJobs++
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit print history import: %w", err)
	}

	return summary, nil
}

func (b *FilamentBridge) printHistoryFingerprintExistsTx(tx *sql.Tx, printerName string, toolheadID int, jobName string, printStarted, printFinished time.Time) (bool, error) {
	var existingID int
	err := tx.QueryRow(`
		SELECT id
		FROM print_history
		WHERE printer_name = ?
		  AND toolhead_id = ?
		  AND job_name = ?
		  AND print_started = ?
		  AND print_finished = ?
		LIMIT 1
	`, printerName, toolheadID, jobName, printStarted, printFinished).Scan(&existingID)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to check imported print history duplicate: %w", err)
	}
	return true, nil
}
