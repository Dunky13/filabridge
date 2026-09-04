package main

import (
	"database/sql"
	"errors"
	"fmt"
)

// PrintToolAssignment snapshots where a slicer's logical tool was physically
// routed, and which spool occupied that input when the print began.
type PrintToolAssignment struct {
	PhysicalToolheadID int                  `json:"physical_toolhead_id"`
	SpoolID            int                  `json:"spool_id,omitempty"`
	Authority          ConsumptionAuthority `json:"consumption_authority,omitempty"`
}

// SetLogicalToolRoute maps a logical tool index from G-code metadata to a
// physical printer input. Identity routing remains the default when no row exists.
func (b *FilamentBridge) SetLogicalToolRoute(printerID string, logicalToolID int, physicalToolheadID int) error {
	if printerID == "" {
		return fmt.Errorf("printer ID is required")
	}
	if logicalToolID < 0 {
		return fmt.Errorf("logical tool ID must be non-negative")
	}
	if physicalToolheadID < 0 {
		return fmt.Errorf("physical toolhead ID must be non-negative")
	}

	_, err := b.db.Exec(`
		INSERT INTO logical_tool_routes (printer_id, logical_tool_id, physical_toolhead_id, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(printer_id, logical_tool_id) DO UPDATE SET
			physical_toolhead_id = excluded.physical_toolhead_id,
			updated_at = excluded.updated_at
	`, printerID, logicalToolID, physicalToolheadID)
	if err != nil {
		return fmt.Errorf("failed to save logical tool route: %w", err)
	}
	return nil
}

// ResolveLogicalToolRoute resolves a slicer tool to a physical input.
func (b *FilamentBridge) ResolveLogicalToolRoute(printerID string, logicalToolID int) (int, error) {
	if printerID == "" {
		return 0, fmt.Errorf("printer ID is required")
	}
	if logicalToolID < 0 {
		return 0, fmt.Errorf("logical tool ID must be non-negative")
	}

	var physicalToolheadID int
	err := b.db.QueryRow(`
		SELECT physical_toolhead_id
		FROM logical_tool_routes
		WHERE printer_id = ? AND logical_tool_id = ?
	`, printerID, logicalToolID).Scan(&physicalToolheadID)
	if err == nil {
		return physicalToolheadID, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return logicalToolID, nil
	}
	return 0, fmt.Errorf("failed to resolve logical tool route: %w", err)
}

// GetLogicalToolRoutes returns explicit overrides only. Callers apply identity
// routing to missing logical tool IDs.
func (b *FilamentBridge) GetLogicalToolRoutes(printerID string) (map[int]int, error) {
	rows, err := b.db.Query(`
		SELECT logical_tool_id, physical_toolhead_id
		FROM logical_tool_routes
		WHERE printer_id = ?
		ORDER BY logical_tool_id
	`, printerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get logical tool routes: %w", err)
	}
	defer rows.Close()

	routes := make(map[int]int)
	for rows.Next() {
		var logicalToolID int
		var physicalToolheadID int
		if err := rows.Scan(&logicalToolID, &physicalToolheadID); err != nil {
			return nil, fmt.Errorf("failed to scan logical tool route: %w", err)
		}
		routes[logicalToolID] = physicalToolheadID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate logical tool routes: %w", err)
	}
	return routes, nil
}

// ResetLogicalToolRoute restores identity routing for one logical tool.
func (b *FilamentBridge) ResetLogicalToolRoute(printerID string, logicalToolID int) error {
	if printerID == "" || logicalToolID < 0 {
		return fmt.Errorf("valid printer ID and logical tool ID are required")
	}
	_, err := b.db.Exec(`DELETE FROM logical_tool_routes WHERE printer_id = ? AND logical_tool_id = ?`, printerID, logicalToolID)
	if err != nil {
		return fmt.Errorf("failed to reset logical tool route: %w", err)
	}
	return nil
}
