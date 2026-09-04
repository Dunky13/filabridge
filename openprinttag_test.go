package main

import (
	"strings"
	"testing"
)

func TestConsumptionUpdatePlanRejectsAutomaticDoubleDebit(t *testing.T) {
	plan := ConsumptionUpdatePlan{
		Authority:              ConsumptionAuthoritySpoolmanLed,
		AutomaticSpoolmanDebit: true,
		AutomaticTagDebit:      true,
	}

	err := plan.Validate()
	if err == nil || !strings.Contains(err.Error(), "double debit") {
		t.Fatalf("Validate() error = %v, want double debit rejection", err)
	}
}

func TestConsumptionUpdatePlanRejectsWriterThatDoesNotOwnAuthority(t *testing.T) {
	tests := []struct {
		name string
		plan ConsumptionUpdatePlan
	}{
		{
			name: "tag writer under Spoolman authority",
			plan: ConsumptionUpdatePlan{
				Authority:         ConsumptionAuthoritySpoolmanLed,
				AutomaticTagDebit: true,
			},
		},
		{
			name: "Spoolman writer under tag authority",
			plan: ConsumptionUpdatePlan{
				Authority:              ConsumptionAuthorityTagLed,
				AutomaticSpoolmanDebit: true,
			},
		},
		{
			name: "writer under observed-only authority",
			plan: ConsumptionUpdatePlan{
				Authority:              ConsumptionAuthorityObservedOnly,
				AutomaticSpoolmanDebit: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.plan.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want authority mismatch rejection")
			}
		})
	}
}

func TestConsumptionUpdatePlanAcceptsSingleAuthorityWriter(t *testing.T) {
	plans := []ConsumptionUpdatePlan{
		{Authority: ConsumptionAuthoritySpoolmanLed, AutomaticSpoolmanDebit: true},
		{Authority: ConsumptionAuthorityTagLed, AutomaticTagDebit: true},
		{Authority: ConsumptionAuthorityObservedOnly},
	}

	for _, plan := range plans {
		if err := plan.Validate(); err != nil {
			t.Errorf("Validate(%+v) error = %v", plan, err)
		}
	}
}

func TestConsumptionUpdatePlanRejectsUnknownAuthority(t *testing.T) {
	plan := ConsumptionUpdatePlan{Authority: ConsumptionAuthority("printer-led")}
	if err := plan.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want unknown authority rejection")
	}
}

func TestParseConsumptionAuthorityMigratesEmptyValueToSpoolmanLed(t *testing.T) {
	authority, err := ParseConsumptionAuthority("")
	if err != nil {
		t.Fatalf("ParseConsumptionAuthority() error = %v", err)
	}
	if authority != ConsumptionAuthoritySpoolmanLed {
		t.Fatalf("authority = %q, want %q", authority, ConsumptionAuthoritySpoolmanLed)
	}
}
