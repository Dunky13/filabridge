package main

import (
	"fmt"
	"strings"
	"time"
)

// CompletedPrintObservation is the stable Print Accounting interface for a
// completion that was observed outside the regular polling state machine.
type CompletedPrintObservation struct {
	PrinterID    string
	JobName      string
	SourcePath   string
	FilamentUsed map[int]float64
	StartedAt    time.Time
	PrintState   string
}

// AccountCompletedPrint persists and accounts a completion through the same
// durable checkpoint implementation used by monitored printers.
func (b *FilamentBridge) AccountCompletedPrint(observation CompletedPrintObservation) error {
	jobLock := b.printerJobLock(observation.PrinterID)
	jobLock.Lock()
	defer jobLock.Unlock()

	configs, err := b.GetAllPrinterConfigs()
	if err != nil {
		return err
	}
	config, exists := configs[observation.PrinterID]
	if !exists {
		return fmt.Errorf("printer %q not found", observation.PrinterID)
	}
	return b.accountCompletedPrint(config, observation)
}

func (b *FilamentBridge) accountCompletedPrint(config PrinterConfig, observation CompletedPrintObservation) error {
	if strings.TrimSpace(observation.PrinterID) == "" {
		return fmt.Errorf("printer ID is required")
	}
	if observation.StartedAt.IsZero() {
		return fmt.Errorf("print start time is required")
	}
	state := strings.ToUpper(strings.TrimSpace(observation.PrintState))
	if state == "" {
		state = StateFinished
	}
	if state != StateFinished && !isAbortPrinterState(state) {
		return fmt.Errorf("completed print state must be %s, %s, or %s", StateFinished, StateStopped, StateError)
	}

	existing, err := b.loadPrinterJobCheckpoint(observation.PrinterID)
	if err != nil {
		return err
	}
	if existing != nil && existing.AccountingStatus != jobAccountingCompleted && existing.AccountingStatus != jobAccountingAborted {
		return fmt.Errorf("printer %s has unresolved job %q (%s)", observation.PrinterID, existing.JobName, existing.AccountingStatus)
	}
	printerName := resolvePrinterName(config)
	assignments, err := b.snapshotToolAssignments(observation.PrinterID, printerName, config.Toolheads)
	if err != nil {
		return err
	}
	checkpoint := &printerJobCheckpoint{
		PrinterID:        observation.PrinterID,
		PrinterName:      printerName,
		SourcePath:       strings.TrimSpace(observation.SourcePath),
		JobName:          strings.TrimSpace(observation.JobName),
		FilamentUsage:    cloneFilamentUsage(observation.FilamentUsed),
		ToolAssignments:  assignments,
		LastState:        state,
		Progress:         100,
		StartedAt:        observation.StartedAt.UTC(),
		AccountingStatus: jobAccountingPending,
		TerminalState:    state,
	}
	if err := b.upsertPrinterJobCheckpoint(checkpoint); err != nil {
		return err
	}
	if isAbortPrinterState(state) {
		return b.abortCheckpoint(checkpoint, config, state)
	}
	return b.finishCheckpoint(checkpoint, config, checkpoint.SourcePath, checkpoint.JobName, checkpoint.FilamentUsage, state)
}
