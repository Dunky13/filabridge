package main

import (
	"fmt"
	"strings"
	"unicode"
)

// CustomPrinterPresetID preserves manual model and material-input settings.
const CustomPrinterPresetID = "custom"

// PrinterPreset is a supported printer configuration. FirmwareModel uses the
// identifiers from Prusa Firmware Buddy; Model is the value stored by FilaBridge.
type PrinterPreset struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Model         string `json:"model"`
	SlicerProfile string `json:"prusa_slicer_profile"`
	FirmwareModel string `json:"firmware_model"`
	Toolheads     int    `json:"toolheads"`
	Group         string `json:"group"`
	Preview       bool   `json:"preview,omitempty"`
}

// Source of truth checked 2026-09-04:
// PrusaSlicer 3.0.0-alpha11 vendor catalog:
// https://github.com/prusa3d/PrusaSlicer/blob/version_3.0.0-alpha11/resources/presets/prusa-research-fff/PrusaResearch/vendor.yaml
// Firmware identifiers, including both INDX targets:
// https://github.com/prusa3d/Prusa-Firmware-Buddy/blob/1ce23f33ed3b94e26aa33a44557d6c4a4be11eb6/ProjectOptions.cmake
// CORE One+ product compatibility (same COREONE firmware/profile family):
// https://www.prusa3d.com/product/indx-conversion-kit-8-toolhead/
// CORE One L+ and its 4/8-tool INDX products:
// https://www.prusa3d.com/product/prusa-core-one-l-indx-8-tool-5/
// CORE One L INDX stays Preview until Prusa enables its currently commented
// PrusaSlicer profiles. Firmware Buddy already defines COREONEL_INDX.
var printerPresetCatalog = []PrinterPreset{
	{ID: "core-one", Name: "Prusa CORE One", Model: "CORE One", SlicerProfile: "c1", FirmwareModel: "COREONE", Toolheads: 1, Group: "CORE One"},
	{ID: "core-one-plus", Name: "Prusa CORE One+ (Gen2)", Model: "CORE One+", SlicerProfile: "c1", FirmwareModel: "COREONE", Toolheads: 1, Group: "CORE One"},
	{ID: "core-one-indx-4t", Name: "Prusa CORE One INDX 4T", Model: "CORE One INDX", SlicerProfile: "c1indx-4t", FirmwareModel: "COREONE_INDX", Toolheads: 4, Group: "CORE One INDX"},
	{ID: "core-one-indx-8t", Name: "Prusa CORE One INDX 8T", Model: "CORE One INDX", SlicerProfile: "c1indx-8t", FirmwareModel: "COREONE_INDX", Toolheads: 8, Group: "CORE One INDX"},
	{ID: "core-one-l", Name: "Prusa CORE One L", Model: "CORE One L", SlicerProfile: "c1l", FirmwareModel: "COREONEL", Toolheads: 1, Group: "CORE One"},
	{ID: "core-one-l-plus", Name: "Prusa CORE One L+", Model: "CORE One L+", SlicerProfile: "c1l", FirmwareModel: "COREONEL", Toolheads: 1, Group: "CORE One"},
	{ID: "core-one-l-indx-4t", Name: "Prusa CORE One L INDX 4T", Model: "CORE One L INDX", SlicerProfile: "c1lindx-4t", FirmwareModel: "COREONEL_INDX", Toolheads: 4, Group: "CORE One INDX", Preview: true},
	{ID: "core-one-l-indx-8t", Name: "Prusa CORE One L INDX 8T", Model: "CORE One L INDX", SlicerProfile: "c1lindx-8t", FirmwareModel: "COREONEL_INDX", Toolheads: 8, Group: "CORE One INDX", Preview: true},
	{ID: "core-one-l-plus-indx-4t", Name: "Prusa CORE One L+ INDX 4T", Model: "CORE One L+ INDX", SlicerProfile: "c1lindx-4t", FirmwareModel: "COREONEL_INDX", Toolheads: 4, Group: "CORE One INDX", Preview: true},
	{ID: "core-one-l-plus-indx-8t", Name: "Prusa CORE One L+ INDX 8T", Model: "CORE One L+ INDX", SlicerProfile: "c1lindx-8t", FirmwareModel: "COREONEL_INDX", Toolheads: 8, Group: "CORE One INDX", Preview: true},
	{ID: "signature-oak", Name: "Prusa Signature Oak", Model: "Signature Oak", SlicerProfile: "c1oak", FirmwareModel: "COREONE", Toolheads: 1, Group: "CORE One"},

	{ID: "xl-1t", Name: "Prusa XL 1T", Model: "XL", SlicerProfile: "xl-1t", FirmwareModel: "XL", Toolheads: 1, Group: "XL"},
	{ID: "xl-2t", Name: "Prusa XL 2T", Model: "XL", SlicerProfile: "xl-2t", FirmwareModel: "XL", Toolheads: 2, Group: "XL"},
	{ID: "xl-5t", Name: "Prusa XL 5T", Model: "XL", SlicerProfile: "xl-5t", FirmwareModel: "XL", Toolheads: 5, Group: "XL"},
	{ID: "xl-plus-1t", Name: "Prusa XL+ 1T", Model: "XL+", SlicerProfile: "xl+1t", FirmwareModel: "XL", Toolheads: 1, Group: "XL"},
	{ID: "xl-plus-2t", Name: "Prusa XL+ 2T", Model: "XL+", SlicerProfile: "xl+2t", FirmwareModel: "XL", Toolheads: 2, Group: "XL"},
	{ID: "xl-plus-5t", Name: "Prusa XL+ 5T", Model: "XL+", SlicerProfile: "xl+5t", FirmwareModel: "XL", Toolheads: 5, Group: "XL"},

	{ID: "mk4s", Name: "Prusa MK4S", Model: "MK4S", SlicerProfile: "mk4s", FirmwareModel: "MK4", Toolheads: 1, Group: "Current bedslingers"},
	{ID: "mk4", Name: "Prusa MK4", Model: "MK4", SlicerProfile: "mk4", FirmwareModel: "MK4", Toolheads: 1, Group: "Current bedslingers"},
	{ID: "mk3.9s", Name: "Prusa MK3.9S", Model: "MK3.9S", SlicerProfile: "mk3.9s", FirmwareModel: "MK4", Toolheads: 1, Group: "Current bedslingers"},
	{ID: "mk3.9", Name: "Prusa MK3.9", Model: "MK3.9", SlicerProfile: "mk3.9", FirmwareModel: "MK4", Toolheads: 1, Group: "Current bedslingers"},
	{ID: "mk3.5", Name: "Prusa MK3.5 / MK3.5S", Model: "MK3.5", SlicerProfile: "mk3.5", FirmwareModel: "MK3.5", Toolheads: 1, Group: "Current bedslingers"},
	{ID: "mini-plus", Name: "Prusa MINI / MINI+", Model: "MINI+", SlicerProfile: "mini", FirmwareModel: "MINI", Toolheads: 1, Group: "Current bedslingers"},

	{ID: "mk3s-plus", Name: "Prusa MK3S+", Model: "MK3S+", SlicerProfile: "mk3s", FirmwareModel: "MK3S", Toolheads: 1, Group: "Legacy"},
	{ID: "mk3s", Name: "Prusa MK3S", Model: "MK3S", SlicerProfile: "mk3s", FirmwareModel: "MK3S", Toolheads: 1, Group: "Legacy"},
}

// PrinterPresets returns an isolated copy safe for callers to sort or mutate.
func PrinterPresets() []PrinterPreset {
	presets := make([]PrinterPreset, len(printerPresetCatalog))
	copy(presets, printerPresetCatalog)
	return presets
}

func findPrinterPreset(id string) (PrinterPreset, bool) {
	for _, preset := range printerPresetCatalog {
		if preset.ID == id {
			return preset, true
		}
	}
	return PrinterPreset{}, false
}

// ApplyPrinterPreset resolves a UI/API preset into authoritative model and
// toolhead values. Blank preset IDs are accepted for existing API clients.
func ApplyPrinterPreset(config PrinterConfig) (PrinterConfig, error) {
	id := strings.TrimSpace(config.PresetID)
	if id == "" {
		config.PresetID = resolvedPrinterPresetID(config.Model, config.Toolheads)
		return config, nil
	}
	config.PresetID = id
	if id == CustomPrinterPresetID {
		return config, nil
	}
	preset, ok := findPrinterPreset(id)
	if !ok {
		return config, fmt.Errorf("unknown printer preset %q", id)
	}
	config.Model = preset.Model
	config.Toolheads = preset.Toolheads
	return config, nil
}

// ResolvePrinterPreset maps persisted pre-preset configurations to catalog
// entries. Unknown/custom model strings intentionally remain unresolved.
func ResolvePrinterPreset(model string, toolheads int) (PrinterPreset, bool) {
	normalized := normalizePrinterModel(model)
	for _, preset := range printerPresetCatalog {
		if preset.Toolheads != toolheads {
			continue
		}
		if normalizePrinterModel(preset.Model) == normalized {
			return preset, true
		}
	}
	for _, preset := range printerPresetCatalog {
		if preset.Toolheads == toolheads && normalizePrinterModel(preset.FirmwareModel) == normalized {
			return preset, true
		}
	}
	return PrinterPreset{}, false
}

func resolvedPrinterPresetID(model string, toolheads int) string {
	preset, ok := ResolvePrinterPreset(model, toolheads)
	if !ok {
		return CustomPrinterPresetID
	}
	return preset.ID
}

// DetectPrinterModel recognizes PrusaSlicer 3 and Firmware Buddy model IDs.
// Specific multi-tool and suffixed models must be checked before base models.
func DetectPrinterModel(hostname string) string {
	value := normalizePrinterModel(hostname)
	patterns := []struct {
		needle string
		model  string
	}{
		{needle: "coreonelplusindx", model: "CORE One L+ INDX"},
		{needle: "coreonelindx", model: "CORE One L INDX"},
		{needle: "coreoneplusindx", model: "CORE One INDX"},
		{needle: "coreoneindx", model: "CORE One INDX"},
		{needle: "coreonelplus", model: "CORE One L+"},
		{needle: "coreonel", model: "CORE One L"},
		{needle: "coreoneplus", model: "CORE One+"},
		{needle: "coreoneoak", model: "Signature Oak"},
		{needle: "signatureoak", model: "Signature Oak"},
		{needle: "coreone", model: "CORE One"},
		{needle: "xlp", model: "XL+"},
		{needle: "xlplus", model: "XL+"},
		{needle: "mk39s", model: "MK3.9S"},
		{needle: "mk39", model: "MK3.9"},
		{needle: "mk35s", model: "MK3.5"},
		{needle: "mk35", model: "MK3.5"},
		{needle: "mk4s", model: "MK4S"},
		{needle: "mk4", model: "MK4"},
		{needle: "mk3splus", model: "MK3S+"},
		{needle: "mk3s", model: "MK3S"},
		{needle: "mini", model: "MINI+"},
		{needle: "xl", model: "XL"},
	}
	for _, pattern := range patterns {
		if strings.Contains(value, pattern.needle) {
			return pattern.model
		}
	}
	return ModelUnknown
}

func normalizePrinterModel(value string) string {
	value = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "+", "plus")
	value = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, value)
	value = strings.TrimPrefix(value, "original")
	return strings.TrimPrefix(value, "prusa")
}
