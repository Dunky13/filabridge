package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ConsumptionAuthority names the sole system allowed to author consumption.
type ConsumptionAuthority string

const (
	ConsumptionAuthoritySpoolmanLed  ConsumptionAuthority = "spoolman-led"
	ConsumptionAuthorityTagLed       ConsumptionAuthority = "tag-led"
	ConsumptionAuthorityObservedOnly ConsumptionAuthority = "observed-only"
)

// ConsumptionUpdatePlan declares automatic consumption writers. Validation is
// required before enabling either writer.
type ConsumptionUpdatePlan struct {
	Authority              ConsumptionAuthority
	AutomaticSpoolmanDebit bool
	AutomaticTagDebit      bool
}

// ParseConsumptionAuthority validates persisted/API configuration. Empty
// values migrate to the historical Spoolman-led behavior.
func ParseConsumptionAuthority(value string) (ConsumptionAuthority, error) {
	authority := ConsumptionAuthority(strings.TrimSpace(value))
	if authority == "" {
		return ConsumptionAuthoritySpoolmanLed, nil
	}
	plan := ConsumptionUpdatePlan{Authority: authority}
	if err := plan.Validate(); err != nil {
		return "", err
	}
	return authority, nil
}

// Validate prevents two systems from debiting the same consumption event.
func (p ConsumptionUpdatePlan) Validate() error {
	if p.AutomaticSpoolmanDebit && p.AutomaticTagDebit {
		return fmt.Errorf("automatic consumption double debit is not allowed")
	}

	switch p.Authority {
	case ConsumptionAuthoritySpoolmanLed:
		if p.AutomaticTagDebit {
			return fmt.Errorf("automatic tag debit is not allowed with %q authority", p.Authority)
		}
	case ConsumptionAuthorityTagLed:
		if p.AutomaticSpoolmanDebit {
			return fmt.Errorf("automatic Spoolman debit is not allowed with %q authority", p.Authority)
		}
	case ConsumptionAuthorityObservedOnly:
		if p.AutomaticSpoolmanDebit || p.AutomaticTagDebit {
			return fmt.Errorf("automatic debit is not allowed with %q authority", p.Authority)
		}
	default:
		return fmt.Errorf("unknown consumption authority %q", p.Authority)
	}

	return nil
}

func (b *FilamentBridge) SetSpoolConsumptionAuthority(spoolID int, authority ConsumptionAuthority) error {
	if spoolID < 1 {
		return fmt.Errorf("spool ID must be positive")
	}
	parsed, err := ParseConsumptionAuthority(string(authority))
	if err != nil {
		return err
	}
	_, err = b.db.Exec(`
		INSERT INTO spool_consumption_authorities (spool_id, authority, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(spool_id) DO UPDATE SET authority = excluded.authority, updated_at = excluded.updated_at
	`, spoolID, parsed)
	if err != nil {
		return fmt.Errorf("failed to save spool consumption authority: %w", err)
	}
	return nil
}

func (b *FilamentBridge) GetSpoolConsumptionAuthority(spoolID int) (ConsumptionAuthority, error) {
	defaultAuthority := ConsumptionAuthoritySpoolmanLed
	if b.config != nil {
		var err error
		defaultAuthority, err = ParseConsumptionAuthority(string(b.config.ConsumptionAuthority))
		if err != nil {
			return "", err
		}
	}
	if spoolID < 1 {
		return defaultAuthority, nil
	}
	var value string
	err := b.db.QueryRow(`SELECT authority FROM spool_consumption_authorities WHERE spool_id = ?`, spoolID).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultAuthority, nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to load spool consumption authority: %w", err)
	}
	return ParseConsumptionAuthority(value)
}
