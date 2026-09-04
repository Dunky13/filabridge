package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type printerIdentity struct {
	ID   string
	Name string
}

// resolvePrinterReference is the compatibility seam for callers that still
// submit a display name. Persistence always receives the stable printer ID.
func (b *FilamentBridge) resolvePrinterReference(reference string) (printerIdentity, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return printerIdentity{}, fmt.Errorf("printer reference is required")
	}

	var identity printerIdentity
	err := b.db.QueryRow("SELECT printer_id, name FROM printer_configs WHERE printer_id = ?", reference).Scan(&identity.ID, &identity.Name)
	if err == nil {
		return identity, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return printerIdentity{}, fmt.Errorf("resolve printer ID %q: %w", reference, err)
	}

	rows, err := b.db.Query("SELECT printer_id, name FROM printer_configs WHERE name = ? ORDER BY printer_id", reference)
	if err != nil {
		return printerIdentity{}, fmt.Errorf("resolve printer name %q: %w", reference, err)
	}
	defer rows.Close()
	var matches []printerIdentity
	for rows.Next() {
		var match printerIdentity
		if err := rows.Scan(&match.ID, &match.Name); err != nil {
			return printerIdentity{}, err
		}
		matches = append(matches, match)
	}
	if err := rows.Err(); err != nil {
		return printerIdentity{}, err
	}
	switch len(matches) {
	case 0:
		return printerIdentity{}, fmt.Errorf("printer %q not found", reference)
	case 1:
		return matches[0], nil
	default:
		return printerIdentity{}, fmt.Errorf("printer name %q is ambiguous; use a printer ID", reference)
	}
}
