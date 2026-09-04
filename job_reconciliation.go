package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

const (
	jobAccountingPending    = "pending"
	jobAccountingProcessing = "processing"
	jobAccountingCompleted  = "completed"
	jobAccountingAborted    = "aborted"
	jobAccountingFailed     = "failed"
	jobAccountingAttention  = "attention"
	jobAccountingLease      = 15 * time.Minute
)

type printerJobCheckpoint struct {
	PrinterID        string
	PrinterName      string
	PrusaLinkJobID   int
	SourcePath       string
	JobName          string
	FilamentUsage    map[int]float64
	ToolAssignments  map[int]PrintToolAssignment
	LastState        string
	Progress         float64
	StartedAt        time.Time
	AccountingStatus string
	TerminalState    string
}

func (b *FilamentBridge) printerJobLock(printerID string) *sync.Mutex {
	b.printerJobLocksMu.Lock()
	defer b.printerJobLocksMu.Unlock()
	lock, exists := b.printerJobLocks[printerID]
	if !exists {
		lock = &sync.Mutex{}
		b.printerJobLocks[printerID] = lock
	}
	return lock
}

func isActivePrinterState(state string) bool {
	switch strings.ToUpper(state) {
	case StatePrinting, StatePaused, StateBusy, StateAttention:
		return true
	default:
		return false
	}
}

func isAbortPrinterState(state string) bool {
	switch strings.ToUpper(state) {
	case StateStopped, StateError:
		return true
	default:
		return false
	}
}

func (b *FilamentBridge) reconcilePrusaLinkJob(printerID string, config PrinterConfig, status *PrusaLinkStatus, jobInfo *PrusaLinkJob, sourcePath string, jobName string, usage map[int]float64) error {
	state := strings.ToUpper(strings.TrimSpace(status.Printer.State))
	if jobInfo != nil && strings.TrimSpace(jobInfo.State) != "" {
		jobState := strings.ToUpper(strings.TrimSpace(jobInfo.State))
		if jobState == StateFinished || isAbortPrinterState(jobState) {
			state = jobState
		}
	}

	if isActivePrinterState(state) {
		jobID := status.Job.ID
		if jobID == 0 && jobInfo != nil {
			jobID = jobInfo.ID
		}
		if jobID == 0 && strings.TrimSpace(sourcePath) == "" && strings.TrimSpace(jobName) == "" {
			checkpoint, err := b.loadPrinterJobCheckpoint(printerID)
			if err != nil || checkpoint == nil {
				return err
			}
			// BUSY and ATTENTION may describe a printer-level transient with no
			// print behind it. Preserve a known job, but never fabricate one that
			// can block the next identified print.
			return b.updateCheckpointObservation(printerID, state, status.Job.Progress, usage)
		}
		return b.saveActiveJobCheckpoint(printerID, config, jobID, sourcePath, jobName, usage, state, status.Job.Progress)
	}

	checkpoint, err := b.loadPrinterJobCheckpoint(printerID)
	if err != nil {
		return err
	}
	if checkpoint == nil {
		return nil
	}
	if (state == StateFinished || isAbortPrinterState(state)) && !terminalObservationMatchesCheckpoint(checkpoint, status, jobInfo, sourcePath) {
		checkpoint.LastState = state
		checkpoint.TerminalState = state
		checkpoint.AccountingStatus = jobAccountingAttention
		return b.upsertPrinterJobCheckpoint(checkpoint)
	}

	if state == StateFinished || ((state == StateIdle || state == StateReady) && max(status.Job.Progress, checkpoint.Progress) >= 99.5) {
		if len(usage) == 0 {
			usage = checkpoint.FilamentUsage
		}
		if sourcePath == "" {
			sourcePath = checkpoint.SourcePath
		}
		if jobName == "" {
			jobName = checkpoint.JobName
		}
		return b.finishCheckpoint(checkpoint, config, sourcePath, jobName, usage, StateFinished)
	}

	if isAbortPrinterState(state) {
		terminalState := state
		return b.abortCheckpoint(checkpoint, config, terminalState)
	}
	if state == StateIdle || state == StateReady {
		// IDLE/READY alone does not say whether the firmware briefly exposed
		// FINISHED or STOPPED between polls. Preserve the checkpoint rather than
		// silently losing usage or charging an aborted job.
		checkpoint.LastState = state
		checkpoint.Progress = max(status.Job.Progress, checkpoint.Progress)
		checkpoint.AccountingStatus = jobAccountingAttention
		checkpoint.TerminalState = state
		if len(usage) > 0 {
			checkpoint.FilamentUsage = usage
		}
		return b.upsertPrinterJobCheckpoint(checkpoint)
	}

	// Unknown firmware states retain the active checkpoint. Future firmware must
	// not silently erase a job before FilaBridge understands its semantics.
	return b.updateCheckpointObservation(printerID, state, status.Job.Progress, usage)
}

func terminalObservationMatchesCheckpoint(checkpoint *printerJobCheckpoint, status *PrusaLinkStatus, jobInfo *PrusaLinkJob, sourcePath string) bool {
	observedJobID := status.Job.ID
	if observedJobID == 0 && jobInfo != nil {
		observedJobID = jobInfo.ID
	}
	if observedJobID != 0 && checkpoint.PrusaLinkJobID != 0 && observedJobID != checkpoint.PrusaLinkJobID {
		return false
	}
	return sourcePath == "" || checkpoint.SourcePath == "" || sourcePath == checkpoint.SourcePath
}

func (b *FilamentBridge) saveActiveJobCheckpoint(printerID string, config PrinterConfig, jobID int, sourcePath string, jobName string, usage map[int]float64, state string, progress float64) error {
	checkpoint, err := b.loadPrinterJobCheckpoint(printerID)
	if err != nil {
		return err
	}

	printerName := resolvePrinterName(config)
	isDifferentJob := checkpoint != nil && (checkpoint.PrusaLinkJobID != jobID || (sourcePath != "" && checkpoint.SourcePath != "" && checkpoint.SourcePath != sourcePath))
	if isDifferentJob && checkpoint.AccountingStatus != jobAccountingCompleted && checkpoint.AccountingStatus != jobAccountingAborted {
		return fmt.Errorf("printer %s has unresolved job %q (%s); refusing to overwrite it with %q", printerID, checkpoint.JobName, checkpoint.AccountingStatus, jobName)
	}
	if checkpoint == nil || checkpoint.AccountingStatus == jobAccountingCompleted || checkpoint.AccountingStatus == jobAccountingAborted || isDifferentJob {
		assignments, err := b.snapshotToolAssignments(printerID, printerName, config.Toolheads)
		if err != nil {
			return err
		}
		checkpoint = &printerJobCheckpoint{
			PrinterID:        printerID,
			PrinterName:      printerName,
			PrusaLinkJobID:   jobID,
			SourcePath:       sourcePath,
			JobName:          jobName,
			FilamentUsage:    usage,
			ToolAssignments:  assignments,
			LastState:        state,
			Progress:         progress,
			StartedAt:        time.Now().UTC(),
			AccountingStatus: jobAccountingPending,
		}
	} else {
		checkpoint.LastState = state
		checkpoint.Progress = progress
		if checkpoint.AccountingStatus == jobAccountingAttention {
			checkpoint.AccountingStatus = jobAccountingPending
			checkpoint.TerminalState = ""
		}
		if sourcePath != "" {
			checkpoint.SourcePath = sourcePath
		}
		if jobName != "" {
			checkpoint.JobName = jobName
		}
		if len(usage) > 0 {
			checkpoint.FilamentUsage = usage
		}
	}

	if err := b.upsertPrinterJobCheckpoint(checkpoint); err != nil {
		return err
	}
	return nil
}

func (b *FilamentBridge) snapshotToolAssignments(printerID string, printerName string, count int) (map[int]PrintToolAssignment, error) {
	if count < 1 {
		count = 1
	}
	assignments := make(map[int]PrintToolAssignment, count)
	for logicalToolID := 0; logicalToolID < count; logicalToolID++ {
		toolheadID, err := b.ResolveLogicalToolRoute(printerID, logicalToolID)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve %s logical tool %d: %w", printerName, logicalToolID, err)
		}
		spoolID, err := b.GetToolheadMapping(printerName, toolheadID)
		if err != nil {
			return nil, fmt.Errorf("failed to snapshot %s toolhead %d: %w", printerName, toolheadID, err)
		}
		authority, err := b.GetSpoolConsumptionAuthority(spoolID)
		if err != nil {
			return nil, fmt.Errorf("failed to snapshot consumption authority for spool %d: %w", spoolID, err)
		}
		assignments[logicalToolID] = PrintToolAssignment{PhysicalToolheadID: toolheadID, SpoolID: spoolID, Authority: authority}
	}
	return assignments, nil
}

func (b *FilamentBridge) upsertPrinterJobCheckpoint(checkpoint *printerJobCheckpoint) error {
	usageJSON, err := json.Marshal(checkpoint.FilamentUsage)
	if err != nil {
		return fmt.Errorf("failed to encode filament usage checkpoint: %w", err)
	}
	assignmentsJSON, err := json.Marshal(checkpoint.ToolAssignments)
	if err != nil {
		return fmt.Errorf("failed to encode tool assignment checkpoint: %w", err)
	}

	_, err = b.db.Exec(`
		INSERT INTO printer_job_checkpoints (
			printer_id, printer_name, prusalink_job_id, source_path, job_name,
			filament_usage_json, tool_assignments_json, last_state, progress,
			started_at, updated_at, accounting_status, terminal_state,
			accounting_owner, accounting_lease_until
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL)
		ON CONFLICT(printer_id) DO UPDATE SET
			printer_name = excluded.printer_name,
			prusalink_job_id = excluded.prusalink_job_id,
			source_path = excluded.source_path,
			job_name = excluded.job_name,
			filament_usage_json = excluded.filament_usage_json,
			tool_assignments_json = excluded.tool_assignments_json,
			last_state = excluded.last_state,
			progress = excluded.progress,
			started_at = excluded.started_at,
			updated_at = excluded.updated_at,
			accounting_status = excluded.accounting_status,
			terminal_state = excluded.terminal_state
		WHERE NOT (
			printer_job_checkpoints.accounting_status = ?
			AND printer_job_checkpoints.accounting_owner IS NOT NULL
			AND printer_job_checkpoints.accounting_owner != ?
			AND printer_job_checkpoints.accounting_lease_until >= ?
		)
	`, checkpoint.PrinterID, checkpoint.PrinterName, checkpoint.PrusaLinkJobID,
		checkpoint.SourcePath, checkpoint.JobName, string(usageJSON), string(assignmentsJSON),
		checkpoint.LastState, checkpoint.Progress, checkpoint.StartedAt, time.Now().UTC(),
		checkpoint.AccountingStatus, nullableString(checkpoint.TerminalState),
		jobAccountingProcessing, b.instanceID, time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("failed to save printer job checkpoint: %w", err)
	}
	return nil
}

func (b *FilamentBridge) loadPrinterJobCheckpoint(printerID string) (*printerJobCheckpoint, error) {
	var checkpoint printerJobCheckpoint
	var usageJSON string
	var assignmentsJSON string
	var terminalState sql.NullString
	err := b.db.QueryRow(`
		SELECT printer_id, printer_name, prusalink_job_id, source_path, job_name,
			filament_usage_json, tool_assignments_json, last_state, progress,
			started_at, accounting_status, terminal_state
		FROM printer_job_checkpoints WHERE printer_id = ?
	`, printerID).Scan(
		&checkpoint.PrinterID, &checkpoint.PrinterName, &checkpoint.PrusaLinkJobID,
		&checkpoint.SourcePath, &checkpoint.JobName, &usageJSON, &assignmentsJSON,
		&checkpoint.LastState, &checkpoint.Progress, &checkpoint.StartedAt,
		&checkpoint.AccountingStatus, &terminalState,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load printer job checkpoint: %w", err)
	}
	if err := json.Unmarshal([]byte(usageJSON), &checkpoint.FilamentUsage); err != nil {
		return nil, fmt.Errorf("failed to decode filament usage checkpoint: %w", err)
	}
	if err := json.Unmarshal([]byte(assignmentsJSON), &checkpoint.ToolAssignments); err != nil {
		return nil, fmt.Errorf("failed to decode tool assignment checkpoint: %w", err)
	}
	if terminalState.Valid {
		checkpoint.TerminalState = terminalState.String
	}
	return &checkpoint, nil
}

func (b *FilamentBridge) updateCheckpointObservation(printerID string, state string, progress float64, usage map[int]float64) error {
	checkpoint, err := b.loadPrinterJobCheckpoint(printerID)
	if err != nil || checkpoint == nil {
		return err
	}
	checkpoint.LastState = state
	checkpoint.Progress = progress
	if len(usage) > 0 {
		checkpoint.FilamentUsage = usage
	}
	return b.upsertPrinterJobCheckpoint(checkpoint)
}

func (b *FilamentBridge) acquireCheckpoint(checkpoint *printerJobCheckpoint) (bool, error) {
	b.mutex.Lock()
	if b.processingPrints[checkpoint.PrinterID] {
		b.mutex.Unlock()
		return false, nil
	}
	b.processingPrints[checkpoint.PrinterID] = true
	b.mutex.Unlock()

	now := time.Now().UTC()
	result, err := b.db.Exec(`
		UPDATE printer_job_checkpoints
		SET accounting_status = ?, accounting_owner = ?, accounting_lease_until = ?, updated_at = ?
		WHERE printer_id = ? AND started_at = ? AND (
			accounting_status IN (?, ?, ?)
			OR (accounting_status = ? AND (accounting_owner = ? OR accounting_lease_until IS NULL OR accounting_lease_until < ?))
		)
	`, jobAccountingProcessing, b.instanceID, now.Add(jobAccountingLease).Unix(), now,
		checkpoint.PrinterID, checkpoint.StartedAt,
		jobAccountingPending, jobAccountingFailed, jobAccountingAttention,
		jobAccountingProcessing, b.instanceID, now.Unix())
	if err != nil {
		b.releaseCheckpointGuard(checkpoint.PrinterID)
		return false, fmt.Errorf("failed to acquire printer job checkpoint: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		b.releaseCheckpointGuard(checkpoint.PrinterID)
	}
	return count == 1, err
}

func (b *FilamentBridge) releaseCheckpointGuard(printerID string) {
	b.mutex.Lock()
	b.processingPrints[printerID] = false
	b.mutex.Unlock()
}

func (b *FilamentBridge) renewCheckpointLease(checkpoint *printerJobCheckpoint) error {
	now := time.Now().UTC()
	result, err := b.db.Exec(`
		UPDATE printer_job_checkpoints
		SET accounting_lease_until = ?, updated_at = ?
		WHERE printer_id = ? AND started_at = ? AND accounting_status = ? AND accounting_owner = ?
	`, now.Add(jobAccountingLease).Unix(), now, checkpoint.PrinterID, checkpoint.StartedAt, jobAccountingProcessing, b.instanceID)
	if err != nil {
		return fmt.Errorf("failed to renew printer job accounting lease: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to verify printer job accounting lease: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("printer job accounting lease is owned by another process")
	}
	return nil
}

func (b *FilamentBridge) finishCheckpoint(checkpoint *printerJobCheckpoint, config PrinterConfig, sourcePath string, jobName string, usage map[int]float64, terminalState string) error {
	acquired, err := b.acquireCheckpoint(checkpoint)
	if err != nil {
		return err
	}
	if !acquired {
		return nil
	}

	if err := b.handlePrusaLinkPrintFinishedWithCheckpoint(config, sourcePath, jobName, usage, checkpoint.ToolAssignments, terminalState, checkpoint); err != nil {
		_ = b.setCheckpointResult(checkpoint, jobAccountingFailed, terminalState)
		return err
	}
	return b.setCheckpointResult(checkpoint, jobAccountingCompleted, terminalState)
}

func (b *FilamentBridge) abortCheckpoint(checkpoint *printerJobCheckpoint, config PrinterConfig, terminalState string) error {
	acquired, err := b.acquireCheckpoint(checkpoint)
	if err != nil {
		return err
	}
	if !acquired {
		return nil
	}

	toolheadID := 0
	var spoolID *int
	if assignment, ok := checkpoint.ToolAssignments[toolheadID]; ok {
		toolheadID = assignment.PhysicalToolheadID
		if assignment.SpoolID > 0 {
			spoolID = cloneIntPointer(&assignment.SpoolID)
		}
	} else {
		toolheadID, spoolID = b.getBestEffortHistoryTarget(checkpoint.PrinterName, config)
	}
	if err := b.renewCheckpointLease(checkpoint); err != nil {
		_ = b.setCheckpointResult(checkpoint, jobAccountingFailed, terminalState)
		return err
	}
	accountingKey := fmt.Sprintf("%s:%d:abort", checkpoint.PrinterID, checkpoint.StartedAt.UnixNano())
	_, err = b.db.Exec(`
		INSERT OR IGNORE INTO print_history (
			printer_id, printer_name_at_event, toolhead_id, spool_id, filament_used, print_started,
			print_finished, job_name, source_path, print_state, accounting_key
		) VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?)
	`, checkpoint.PrinterID, checkpoint.PrinterName, toolheadID, spoolID, checkpoint.StartedAt,
		time.Now().UTC(), checkpoint.JobName, checkpoint.SourcePath, terminalState, accountingKey)
	if err != nil {
		_ = b.setCheckpointResult(checkpoint, jobAccountingFailed, terminalState)
		return fmt.Errorf("failed to record aborted print: %w", err)
	}
	return b.setCheckpointResult(checkpoint, jobAccountingAborted, terminalState)
}

func (b *FilamentBridge) setCheckpointResult(checkpoint *printerJobCheckpoint, accountingStatus string, terminalState string) error {
	defer b.releaseCheckpointGuard(checkpoint.PrinterID)
	result, err := b.db.Exec(`
		UPDATE printer_job_checkpoints
		SET accounting_status = ?, terminal_state = ?, last_state = ?,
			accounting_owner = NULL, accounting_lease_until = NULL, updated_at = ?
		WHERE printer_id = ? AND started_at = ? AND accounting_status = ? AND accounting_owner = ?
	`, accountingStatus, terminalState, terminalState, time.Now().UTC(), checkpoint.PrinterID, checkpoint.StartedAt, jobAccountingProcessing, b.instanceID)
	if err != nil {
		return fmt.Errorf("failed to finalize printer job checkpoint: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to verify printer job checkpoint finalization: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("printer job checkpoint changed before finalization")
	}

	return nil
}

func nullableString(value string) interface{} {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func (b *FilamentBridge) processDurableJobAdjustment(checkpoint *printerJobCheckpoint, logicalToolID int, physicalToolheadID int, spoolID int, filamentUsed float64, authority ConsumptionAuthority, jobName string, sourcePath string, printState string) error {
	spoolman := b.spoolmanSnapshot()
	if err := b.renewCheckpointLease(checkpoint); err != nil {
		return err
	}
	now := time.Now().UTC()
	var nullableSpoolID interface{}
	if spoolID > 0 {
		nullableSpoolID = spoolID
	}
	_, err := b.db.Exec(`
		INSERT OR IGNORE INTO printer_job_adjustments (
			printer_id, checkpoint_started_at, logical_tool_id, physical_toolhead_id,
			spool_id, filament_used, authority, status, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, checkpoint.PrinterID, checkpoint.StartedAt, logicalToolID, physicalToolheadID,
		nullableSpoolID, filamentUsed, authority, jobAccountingPending, now)
	if err != nil {
		return fmt.Errorf("failed to initialize durable tool adjustment: %w", err)
	}

	var storedPhysicalToolheadID int
	var storedSpoolID sql.NullInt64
	var storedFilamentUsed float64
	var storedAuthority string
	var status string
	var before sql.NullFloat64
	var after sql.NullFloat64
	err = b.db.QueryRow(`
		SELECT physical_toolhead_id, spool_id, filament_used, authority, status,
			used_weight_before, used_weight_after
		FROM printer_job_adjustments
		WHERE printer_id = ? AND checkpoint_started_at = ? AND logical_tool_id = ?
	`, checkpoint.PrinterID, checkpoint.StartedAt, logicalToolID).Scan(
		&storedPhysicalToolheadID, &storedSpoolID, &storedFilamentUsed, &storedAuthority,
		&status, &before, &after,
	)
	if err != nil {
		return fmt.Errorf("failed to load durable tool adjustment: %w", err)
	}
	if status == jobAccountingCompleted {
		return nil
	}
	if storedPhysicalToolheadID != physicalToolheadID || storedSpoolID.Int64 != int64(spoolID) || math.Abs(storedFilamentUsed-filamentUsed) > 0.0001 || storedAuthority != string(authority) {
		return fmt.Errorf("durable tool adjustment changed for logical tool %d; manual reconciliation required", logicalToolID)
	}

	if authority == ConsumptionAuthoritySpoolmanLed && spoolID > 0 {
		spool, err := spoolman.GetSpool(spoolID)
		if err != nil {
			return fmt.Errorf("failed to inspect spool %d before durable update: %w", spoolID, err)
		}
		if !before.Valid || !after.Valid {
			before = sql.NullFloat64{Float64: spool.UsedWeight, Valid: true}
			after = sql.NullFloat64{Float64: spool.UsedWeight + filamentUsed, Valid: true}
			_, err = b.db.Exec(`
				UPDATE printer_job_adjustments
				SET used_weight_before = ?, used_weight_after = ?, status = ?, last_error = NULL, updated_at = ?
				WHERE printer_id = ? AND checkpoint_started_at = ? AND logical_tool_id = ?
			`, before.Float64, after.Float64, jobAccountingPending, now,
				checkpoint.PrinterID, checkpoint.StartedAt, logicalToolID)
			if err != nil {
				return fmt.Errorf("failed to prepare durable spool update: %w", err)
			}
		}

		alreadyApplied := math.Abs(spool.UsedWeight-after.Float64) <= 0.001
		canApply := math.Abs(spool.UsedWeight-before.Float64) <= 0.001
		if !alreadyApplied && !canApply {
			message := fmt.Sprintf("spool %d changed from expected %.3fg/%.3fg to %.3fg; manual reconciliation required", spoolID, before.Float64, after.Float64, spool.UsedWeight)
			_, _ = b.db.Exec(`
				UPDATE printer_job_adjustments SET status = ?, last_error = ?, updated_at = ?
				WHERE printer_id = ? AND checkpoint_started_at = ? AND logical_tool_id = ?
			`, StateAttention, message, time.Now().UTC(), checkpoint.PrinterID, checkpoint.StartedAt, logicalToolID)
			return errors.New(message)
		}
		if canApply && !alreadyApplied {
			if err := b.renewCheckpointLease(checkpoint); err != nil {
				return err
			}
			_, err = b.db.Exec(`
				UPDATE printer_job_adjustments SET status = ?, updated_at = ?
				WHERE printer_id = ? AND checkpoint_started_at = ? AND logical_tool_id = ?
			`, jobAccountingProcessing, time.Now().UTC(), checkpoint.PrinterID, checkpoint.StartedAt, logicalToolID)
			if err != nil {
				return fmt.Errorf("failed to acquire durable spool update: %w", err)
			}
			update := map[string]interface{}{
				"used_weight": after.Float64,
				"last_used":   time.Now().UTC().Format(time.RFC3339),
			}
			if spool.FirstUsed == "" {
				update["first_used"] = time.Now().UTC().Format(time.RFC3339)
			}
			if err := spoolman.UpdateSpool(spoolID, update); err != nil {
				message := fmt.Sprintf("durable update of spool %d failed: %v", spoolID, err)
				_, _ = b.db.Exec(`
					UPDATE printer_job_adjustments SET last_error = ?, updated_at = ?
					WHERE printer_id = ? AND checkpoint_started_at = ? AND logical_tool_id = ?
				`, message, time.Now().UTC(), checkpoint.PrinterID, checkpoint.StartedAt, logicalToolID)
				return errors.New(message)
			}
		}
	}

	accountingKey := fmt.Sprintf("%s:%d:%d", checkpoint.PrinterID, checkpoint.StartedAt.UnixNano(), logicalToolID)
	if err := b.renewCheckpointLease(checkpoint); err != nil {
		return err
	}
	tx, err := b.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin durable history update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`
		INSERT OR IGNORE INTO print_history (
			printer_id, printer_name_at_event, toolhead_id, spool_id, filament_used, print_started,
			print_finished, job_name, source_path, print_state, accounting_key
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, checkpoint.PrinterID, checkpoint.PrinterName, physicalToolheadID, nullableSpoolID, filamentUsed,
		checkpoint.StartedAt, time.Now().UTC(), jobName, sourcePath, printState, accountingKey)
	if err != nil {
		return fmt.Errorf("failed to record durable print history: %w", err)
	}
	_, err = tx.Exec(`
		UPDATE printer_job_adjustments
		SET status = ?, last_error = NULL, updated_at = ?
		WHERE printer_id = ? AND checkpoint_started_at = ? AND logical_tool_id = ?
	`, jobAccountingCompleted, time.Now().UTC(), checkpoint.PrinterID, checkpoint.StartedAt, logicalToolID)
	if err != nil {
		return fmt.Errorf("failed to complete durable tool adjustment: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit durable tool adjustment: %w", err)
	}
	return nil
}

func (b *FilamentBridge) resolvePrinterJobCheckpoint(printerID string, outcome string) error {
	jobLock := b.printerJobLock(printerID)
	jobLock.Lock()
	defer jobLock.Unlock()

	checkpoint, err := b.loadPrinterJobCheckpoint(printerID)
	if err != nil {
		return err
	}
	if checkpoint == nil {
		return fmt.Errorf("printer has no job checkpoint")
	}
	configs, err := b.GetAllPrinterConfigs()
	if err != nil {
		return err
	}
	config, exists := configs[printerID]
	if !exists {
		return fmt.Errorf("printer configuration not found")
	}
	switch strings.ToUpper(strings.TrimSpace(outcome)) {
	case StateFinished:
		return b.finishCheckpoint(checkpoint, config, checkpoint.SourcePath, checkpoint.JobName, checkpoint.FilamentUsage, StateFinished)
	case StateStopped:
		return b.abortCheckpoint(checkpoint, config, StateStopped)
	default:
		return fmt.Errorf("outcome must be %s or %s", StateFinished, StateStopped)
	}
}
