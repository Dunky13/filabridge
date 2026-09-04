package main

import "testing"

func TestPrinterPresetsIncludeCurrentAndPreviewPrusaConfigurations(t *testing.T) {
	t.Parallel()

	expected := map[string]struct {
		model     string
		toolheads int
		preview   bool
	}{
		"core-one":                {model: "CORE One", toolheads: 1},
		"core-one-plus":           {model: "CORE One+", toolheads: 1},
		"core-one-indx-8t":        {model: "CORE One INDX", toolheads: 8},
		"core-one-l":              {model: "CORE One L", toolheads: 1},
		"core-one-l-plus":         {model: "CORE One L+", toolheads: 1},
		"core-one-l-indx-8t":      {model: "CORE One L INDX", toolheads: 8, preview: true},
		"core-one-l-plus-indx-8t": {model: "CORE One L+ INDX", toolheads: 8, preview: true},
		"xl-plus-5t":              {model: "XL+", toolheads: 5},
		"mk4s":                    {model: "MK4S", toolheads: 1},
		"mini-plus":               {model: "MINI+", toolheads: 1},
	}

	presets := PrinterPresets()
	byID := make(map[string]PrinterPreset, len(presets))
	for _, preset := range presets {
		byID[preset.ID] = preset
	}

	for id, want := range expected {
		preset, ok := byID[id]
		if !ok {
			t.Fatalf("preset %q missing", id)
		}
		if preset.Model != want.model || preset.Toolheads != want.toolheads || preset.Preview != want.preview {
			t.Errorf("preset %q = %#v, want model=%q toolheads=%d preview=%t", id, preset, want.model, want.toolheads, want.preview)
		}
	}
}

func TestINDXPresetsExposeUpstreamSlicerAndFirmwareIdentifiers(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		slicer   string
		firmware string
	}{
		"core-one-indx-8t":   {slicer: "c1indx-8t", firmware: "COREONE_INDX"},
		"core-one-l-indx-8t": {slicer: "c1lindx-8t", firmware: "COREONEL_INDX"},
	}
	for id, want := range tests {
		preset, ok := findPrinterPreset(id)
		if !ok {
			t.Fatalf("preset %q missing", id)
		}
		if preset.SlicerProfile != want.slicer || preset.FirmwareModel != want.firmware {
			t.Errorf("preset %q identifiers = %q/%q, want %q/%q", id, preset.SlicerProfile, preset.FirmwareModel, want.slicer, want.firmware)
		}
	}
}

func TestResolvePrinterPresetMigratesStoredModelAndToolheadPairs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		model     string
		toolheads int
		wantID    string
	}{
		{name: "legacy core one", model: "CORE One", toolheads: 1, wantID: "core-one"},
		{name: "legacy xl five tool", model: "XL", toolheads: 5, wantID: "xl-5t"},
		{name: "firmware core one indx id", model: "COREONE_INDX", toolheads: 8, wantID: "core-one-indx-8t"},
		{name: "firmware core one l indx id", model: "COREONEL_INDX", toolheads: 8, wantID: "core-one-l-indx-8t"},
		{name: "case and separator tolerant", model: "prusa-core-one-l", toolheads: 1, wantID: "core-one-l"},
		{name: "legacy original prusa prefix", model: "Original Prusa CORE One", toolheads: 1, wantID: "core-one"},
		{name: "exact model wins over shared firmware family", model: "MK4", toolheads: 1, wantID: "mk4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preset, ok := ResolvePrinterPreset(tt.model, tt.toolheads)
			if !ok {
				t.Fatalf("ResolvePrinterPreset(%q, %d) did not resolve", tt.model, tt.toolheads)
			}
			if preset.ID != tt.wantID {
				t.Errorf("preset ID = %q, want %q", preset.ID, tt.wantID)
			}
		})
	}
}

func TestApplyPrinterPresetUsesAuthoritativeModelAndToolheadCount(t *testing.T) {
	t.Parallel()

	config, err := ApplyPrinterPreset(PrinterConfig{
		Name:      "Workshop",
		PresetID:  "core-one-indx-8t",
		Model:     "wrong",
		IPAddress: "core-one.local",
		APIKey:    "secret",
		Toolheads: 1,
	})
	if err != nil {
		t.Fatalf("ApplyPrinterPreset returned error: %v", err)
	}
	if config.Model != "CORE One INDX" || config.Toolheads != 8 {
		t.Fatalf("config = %#v, want CORE One INDX with 8 toolheads", config)
	}
}

func TestApplyPrinterPresetKeepsCustomPrinterSettings(t *testing.T) {
	t.Parallel()

	config, err := ApplyPrinterPreset(PrinterConfig{
		PresetID:  CustomPrinterPresetID,
		Model:     "Voron 2.4",
		Toolheads: 3,
	})
	if err != nil {
		t.Fatalf("ApplyPrinterPreset returned error: %v", err)
	}
	if config.Model != "Voron 2.4" || config.Toolheads != 3 {
		t.Fatalf("custom config changed: %#v", config)
	}
}

func TestApplyPrinterPresetRejectsUnknownPreset(t *testing.T) {
	t.Parallel()

	_, err := ApplyPrinterPreset(PrinterConfig{PresetID: "not-a-real-printer"})
	if err == nil {
		t.Fatal("ApplyPrinterPreset accepted unknown preset")
	}
}

func TestDetectPrinterModelUsesSpecificFirmwareIdentifiersFirst(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"CORE One L+ INDX":  "CORE One L+ INDX",
		"COREONEL_INDX-123": "CORE One L INDX",
		"CORE One+ INDX":    "CORE One INDX",
		"COREONE_INDX":      "CORE One INDX",
		"COREONEL":          "CORE One L",
		"COREONE":           "CORE One",
		"COREONEOAK":        "Signature Oak",
		"XLP5T":             "XL+",
		"MK3.9S":            "MK3.9S",
	}

	for hostname, want := range tests {
		if got := detectPrinterModel(hostname); got != want {
			t.Errorf("detectPrinterModel(%q) = %q, want %q", hostname, got, want)
		}
	}
}
