// Package profilesync exports Spoolman filament definitions as managed
// PrusaSlicer 3 user profiles and installs those profiles safely.
package profilesync

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// ManagedProfilePath is relative to a PrusaSlicer 3 data directory. Keeping
	// the profile in Prusa Research's user namespace makes Generic material
	// inheritance resolve without replacing the official preset repository.
	ManagedProfilePath = "presets/user/prusa-research-fff/PrusaResearch/preset-filament-filabridge-spoolman.yaml"
	managedMarker      = "# Managed by FilaBridge; local edits to this file will be replaced."
)

// Filament is inventory-independent material metadata read from Spoolman.
// Physical spool IDs, locations, and remaining quantities intentionally do not
// belong here.
type Filament struct {
	ID              int
	Name            string
	Vendor          string
	Material        string
	Density         float64
	Diameter        float64
	SpoolWeight     float64
	CostPerKilogram float64
	ColorHex        string
	Extra           map[string]string
}

// Manifest describes one deterministic managed-profile bundle.
type Manifest struct {
	SchemaVersion    int    `json:"schema_version"`
	Version          string `json:"version"`
	PrusaSlicerMajor int    `json:"prusa_slicer_major"`
	ManagedProfile   string `json:"managed_profile"`
	ProfileSHA256    string `json:"profile_sha256"`
	FilamentIDs      []int  `json:"filament_ids"`
}

var genericBaseProfiles = map[string]string{
	"ABS":   "Generic ABS",
	"ASA":   "Generic ASA",
	"BVOH":  "Generic BVOH",
	"CPE":   "Generic CPE",
	"FLEX":  "Generic FLEX",
	"HIPS":  "Generic HIPS",
	"PA":    "Generic PA",
	"PC":    "Generic PC",
	"PCCF":  "Generic PC-CF",
	"PCTG":  "Generic PCTG",
	"PEBA":  "Generic PEBA",
	"PETG":  "Generic PETG",
	"PLA":   "Generic PLA",
	"PP":    "Generic PP",
	"PPCF":  "Generic PPCF",
	"PVA":   "Generic PVA",
	"PVB":   "Generic PVB",
	"TPE":   "Generic FLEX",
	"TPU":   "Generic FLEX",
	"NYLON": "Generic PA",
}

// Export builds a byte-for-byte deterministic ZIP. Version is derived from
// canonical profile input, never wall-clock time.
func Export(filaments []Filament) ([]byte, Manifest, error) {
	canonical := append([]Filament(nil), filaments...)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].ID < canonical[j].ID })
	for index, filament := range canonical {
		if filament.ID <= 0 {
			return nil, Manifest{}, fmt.Errorf("filament at index %d has invalid Spoolman filament ID %d", index, filament.ID)
		}
		if index > 0 && canonical[index-1].ID == filament.ID {
			return nil, Manifest{}, fmt.Errorf("duplicate Spoolman filament ID %d", filament.ID)
		}
	}

	body, err := renderProfiles(canonical, "")
	if err != nil {
		return nil, Manifest{}, err
	}
	versionHash := sha256.Sum256(body)
	version := hex.EncodeToString(versionHash[:])[:16]
	profile, err := renderProfiles(canonical, version)
	if err != nil {
		return nil, Manifest{}, err
	}
	profileHash := sha256.Sum256(profile)
	manifest := Manifest{
		SchemaVersion: 1, Version: version, PrusaSlicerMajor: 3,
		ManagedProfile: ManagedProfilePath,
		ProfileSHA256:  hex.EncodeToString(profileHash[:]),
		FilamentIDs:    make([]int, 0, len(canonical)),
	}
	for _, filament := range canonical {
		manifest.FilamentIDs = append(manifest.FilamentIDs, filament.ID)
	}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("encode profile manifest: %w", err)
	}
	manifestJSON = append(manifestJSON, '\n')

	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, file := range []struct {
		name string
		data []byte
	}{{name: "manifest.json", data: manifestJSON}, {name: ManagedProfilePath, data: profile}} {
		header := &zip.FileHeader{Name: file.name, Method: zip.Store}
		header.SetMode(0o644)
		header.Modified = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)
		entry, createErr := writer.CreateHeader(header)
		if createErr != nil {
			_ = writer.Close()
			return nil, Manifest{}, fmt.Errorf("create %s in profile archive: %w", file.name, createErr)
		}
		if _, writeErr := entry.Write(file.data); writeErr != nil {
			_ = writer.Close()
			return nil, Manifest{}, fmt.Errorf("write %s to profile archive: %w", file.name, writeErr)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, Manifest{}, fmt.Errorf("finish profile archive: %w", err)
	}
	if output.Len() > MaxBundleBytes {
		return nil, Manifest{}, fmt.Errorf("profile archive is too large: maximum is %d bytes", MaxBundleBytes)
	}
	return output.Bytes(), manifest, nil
}

func renderProfiles(filaments []Filament, version string) ([]byte, error) {
	var output strings.Builder
	output.WriteString(managedMarker)
	output.WriteByte('\n')
	if version != "" {
		output.WriteString("# bundle-version: ")
		output.WriteString(version)
		output.WriteByte('\n')
	}
	for _, filament := range filaments {
		base, err := baseProfile(filament)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSpace(filament.Name)
		if name == "" {
			name = fmt.Sprintf("Filament %d", filament.ID)
		}
		vendor := strings.TrimSpace(filament.Vendor)
		displayName := "FilaBridge - " + name + " [SM#" + strconv.Itoa(filament.ID) + "]"
		if vendor != "" {
			displayName = "FilaBridge - " + vendor + " - " + name + " [SM#" + strconv.Itoa(filament.ID) + "]"
		}
		material := canonicalMaterial(filament.Material)

		output.WriteString("---\nkind: filament\ninherits:\n- ")
		output.WriteString(yamlString(base))
		output.WriteString("\nname: ")
		output.WriteString(yamlString(displayName))
		output.WriteString("\nid: ")
		output.WriteString(yamlString(fmt.Sprintf("filabridge:spoolman-filament:%d", filament.ID)))
		output.WriteString("\nvalues:\n")
		writeYAMLValue(&output, "filament_settings_id", displayName)
		writeYAMLValue(&output, "filament_notes", fmt.Sprintf("Managed from Spoolman filament definition %d by FilaBridge", filament.ID))
		if vendor != "" {
			writeYAMLValue(&output, "filament_vendor", vendor)
		}
		if material != "" {
			writeYAMLValue(&output, "filament_type", material)
		}
		if color := normalizeColor(filament.ColorHex); color != "" {
			writeYAMLValue(&output, "filament_colour", color)
		}
		writePositiveFloat(&output, "filament_density", filament.Density)
		writePositiveFloat(&output, "filament_diameter", filament.Diameter)
		writePositiveFloat(&output, "filament_spool_weight", filament.SpoolWeight)
		writePositiveFloat(&output, "filament_cost", filament.CostPerKilogram)
	}
	return []byte(output.String()), nil
}

func baseProfile(filament Filament) (string, error) {
	if explicit := strings.TrimSpace(filament.Extra["prusaslicer_base_profile"]); explicit != "" {
		return explicit, nil
	}
	material := canonicalMaterial(filament.Material)
	if base := genericBaseProfiles[material]; base != "" {
		return base, nil
	}
	return "", fmt.Errorf("Spoolman filament %d (%q) material %q has no safe PrusaSlicer base; set extra.prusaslicer_base_profile", filament.ID, filament.Name, filament.Material)
}

func canonicalMaterial(material string) string {
	value := strings.ToUpper(strings.TrimSpace(material))
	value = strings.NewReplacer("-", "", "_", "", " ", "").Replace(value)
	return value
}

func normalizeColor(color string) string {
	value := strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(color), "#"))
	if len(value) != 6 && len(value) != 8 {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return "#" + value
}

func yamlString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func writeYAMLValue(output *strings.Builder, key, value string) {
	output.WriteString("  ")
	output.WriteString(key)
	output.WriteString(": ")
	output.WriteString(yamlString(value))
	output.WriteByte('\n')
}

func writePositiveFloat(output *strings.Builder, key string, value float64) {
	if value <= 0 {
		return
	}
	writeYAMLValue(output, key, strconv.FormatFloat(value, 'f', -1, 64))
}
