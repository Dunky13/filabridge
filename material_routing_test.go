package main

import "testing"

func TestProcessFilamentUsageUsesConfiguredLogicalToolRoute(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()

	bridge := newTestBridge(t, spoolman.server.URL)
	if err := bridge.SetToolheadMapping("Printer A", 7, 20); err != nil {
		t.Fatalf("SetToolheadMapping() error = %v", err)
	}
	if err := bridge.SetLogicalToolRoute("printer-a", 0, 7); err != nil {
		t.Fatalf("SetLogicalToolRoute() error = %v", err)
	}

	if err := bridge.processFilamentUsage("Printer A", map[int]float64{0: 12.5}, "routed.bgcode", "usb/routed.bgcode"); err != nil {
		t.Fatalf("processFilamentUsage() error = %v", err)
	}

	if got := spoolman.usedWeight(20); got != 17.5 {
		t.Fatalf("spool used weight = %.2f, want 17.50", got)
	}
	history, err := bridge.GetPrintHistory(10)
	if err != nil {
		t.Fatalf("GetPrintHistory() error = %v", err)
	}
	if len(history) != 1 || history[0].ToolheadID != 7 {
		t.Fatalf("history = %+v, want physical toolhead 7", history)
	}
}

func TestLogicalToolRoutesDefaultToIdentityAndValidateInputs(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()
	bridge := newTestBridge(t, spoolman.server.URL)

	physical, err := bridge.ResolveLogicalToolRoute("Printer A", 3)
	if err != nil {
		t.Fatalf("ResolveLogicalToolRoute() error = %v", err)
	}
	if physical != 3 {
		t.Fatalf("default physical toolhead = %d, want 3", physical)
	}
	if err := bridge.SetLogicalToolRoute("Printer A", -1, 0); err == nil {
		t.Fatal("SetLogicalToolRoute() accepted negative logical tool")
	}
	if err := bridge.SetLogicalToolRoute("Printer A", 0, -1); err == nil {
		t.Fatal("SetLogicalToolRoute() accepted negative physical toolhead")
	}
}

func TestLogicalToolRouteSurvivesPrinterRename(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()
	bridge := newTestBridge(t, spoolman.server.URL)
	config := PrinterConfig{Name: "Before", Model: "CORE One INDX", IPAddress: "printer.local", Toolheads: 8}
	if err := bridge.SavePrinterConfig("printer-a", config); err != nil {
		t.Fatalf("SavePrinterConfig(before) error = %v", err)
	}
	if err := bridge.SetLogicalToolRoute("printer-a", 0, 7); err != nil {
		t.Fatalf("SetLogicalToolRoute() error = %v", err)
	}
	config.Name = "After"
	if err := bridge.SavePrinterConfig("printer-a", config); err != nil {
		t.Fatalf("SavePrinterConfig(after) error = %v", err)
	}
	physical, err := bridge.ResolveLogicalToolRoute("printer-a", 0)
	if err != nil || physical != 7 {
		t.Fatalf("ResolveLogicalToolRoute() = %d, %v; want 7", physical, err)
	}
}

func TestObservedOnlyAuthorityRecordsUsageWithoutDebitingSpoolman(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()
	bridge := newTestBridge(t, spoolman.server.URL)
	config := bridge.GetConfigSnapshot()
	config.ConsumptionAuthority = ConsumptionAuthorityObservedOnly
	if err := bridge.UpdateConfig(config); err != nil {
		t.Fatalf("UpdateConfig() error = %v", err)
	}
	if err := bridge.SetToolheadMapping("Printer A", 0, 20); err != nil {
		t.Fatalf("SetToolheadMapping() error = %v", err)
	}

	if err := bridge.processFilamentUsage("Printer A", map[int]float64{0: 12.5}, "observed.bgcode", "usb/observed.bgcode"); err != nil {
		t.Fatalf("processFilamentUsage() error = %v", err)
	}
	if got := spoolman.usedWeight(20); got != 5 {
		t.Fatalf("spool used weight = %.2f, want unchanged 5.00", got)
	}
	history, err := bridge.GetPrintHistory(10)
	if err != nil {
		t.Fatalf("GetPrintHistory() error = %v", err)
	}
	if len(history) != 1 || history[0].FilamentUsed != 12.5 {
		t.Fatalf("observed history = %+v, want recorded 12.5g", history)
	}
}

func TestPerSpoolAuthorityIsSnapshottedForActiveJob(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()
	bridge := newTestBridge(t, spoolman.server.URL)
	if err := bridge.SetToolheadMapping("Printer A", 0, 20); err != nil {
		t.Fatalf("SetToolheadMapping() error = %v", err)
	}
	if err := bridge.SetSpoolConsumptionAuthority(20, ConsumptionAuthorityTagLed); err != nil {
		t.Fatalf("SetSpoolConsumptionAuthority(tag-led) error = %v", err)
	}
	printer := PrinterConfig{Name: "Printer A", IPAddress: "printer.local", Toolheads: 1}
	usage := map[int]float64{0: 12.5}
	if err := bridge.saveActiveJobCheckpoint("printer-a", printer, 42, "usb/tag-led.bgcode", "tag-led.bgcode", usage, StatePrinting, 100); err != nil {
		t.Fatalf("saveActiveJobCheckpoint() error = %v", err)
	}
	checkpoint, err := bridge.loadPrinterJobCheckpoint("printer-a")
	if err != nil || checkpoint == nil {
		t.Fatalf("checkpoint = %#v, %v", checkpoint, err)
	}
	if checkpoint.ToolAssignments[0].Authority != ConsumptionAuthorityTagLed {
		t.Fatalf("snapshotted authority = %q, want %q", checkpoint.ToolAssignments[0].Authority, ConsumptionAuthorityTagLed)
	}
	if err := bridge.SetSpoolConsumptionAuthority(20, ConsumptionAuthoritySpoolmanLed); err != nil {
		t.Fatalf("SetSpoolConsumptionAuthority(spoolman-led) error = %v", err)
	}
	if err := bridge.finishCheckpoint(checkpoint, printer, checkpoint.SourcePath, checkpoint.JobName, usage, StateFinished); err != nil {
		t.Fatalf("finishCheckpoint() error = %v", err)
	}
	if got := spoolman.usedWeight(20); got != 5 {
		t.Fatalf("spool used weight = %.2f, want unchanged 5.00", got)
	}
}
