package profilesync_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"filabridge/profilesync"
	"github.com/goccy/go-yaml"
)

func TestExportBuildsDeterministicManagedProfilesFromFilamentDefinitions(t *testing.T) {
	filaments := []profilesync.Filament{
		{ID: 42, Name: "Galaxy Black", Vendor: "Prusa Polymers", Material: "PLA", Density: 1.24, Diameter: 1.75, SpoolWeight: 201, CostPerKilogram: 29.99, ColorHex: "0a0b0c"},
		{ID: 7, Name: "Flex 95A", Vendor: "Example", Material: "TPU", Density: 1.22, Diameter: 1.75, ColorHex: "ff00aa"},
	}

	first, firstManifest, err := profilesync.Export(filaments)
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	second, secondManifest, err := profilesync.Export([]profilesync.Filament{filaments[1], filaments[0]})
	if err != nil {
		t.Fatalf("Export(reordered) error = %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("Export() is not deterministic across input order")
	}
	if !reflect.DeepEqual(firstManifest, secondManifest) {
		t.Fatalf("manifest changed across input order: %#v != %#v", firstManifest, secondManifest)
	}
	if firstManifest.SchemaVersion != 1 || firstManifest.PrusaSlicerMajor != 3 || len(firstManifest.Version) != 16 {
		t.Fatalf("manifest metadata = %#v", firstManifest)
	}
	if got, want := firstManifest.FilamentIDs, []int{7, 42}; !reflect.DeepEqual(got, want) {
		t.Fatalf("filament IDs = %v, want %v", got, want)
	}

	files := unzipFiles(t, first)
	if got, want := sortedKeys(files), []string{"manifest.json", profilesync.ManagedProfilePath}; !reflect.DeepEqual(got, want) {
		t.Fatalf("archive files = %v, want %v", got, want)
	}
	var archiveManifest profilesync.Manifest
	if err := json.Unmarshal(files["manifest.json"], &archiveManifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if !reflect.DeepEqual(archiveManifest, firstManifest) {
		t.Fatalf("archive manifest = %#v, want %#v", archiveManifest, firstManifest)
	}

	profileYAML := string(files[profilesync.ManagedProfilePath])
	for _, want := range []string{
		"# Managed by FilaBridge; local edits to this file will be replaced.",
		`id: "filabridge:spoolman-filament:7"`,
		`inherits:`,
		`- "Generic FLEX"`,
		`id: "filabridge:spoolman-filament:42"`,
		`name: "FilaBridge - Prusa Polymers - Galaxy Black [SM#42]"`,
		`- "Generic PLA"`,
		`filament_colour: "#0A0B0C"`,
		`filament_cost: "29.99"`,
	} {
		if !strings.Contains(profileYAML, want) {
			t.Errorf("profile YAML missing %q:\n%s", want, profileYAML)
		}
	}
	for _, forbidden := range []string{"remaining_weight", "spool_id", "location_id"} {
		if strings.Contains(profileYAML, forbidden) {
			t.Errorf("profile YAML contains physical-spool field %q", forbidden)
		}
	}
	decoder := yaml.NewDecoder(strings.NewReader(profileYAML))
	decodedProfiles := 0
	for {
		var profile map[string]interface{}
		err := decoder.Decode(&profile)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("generated profile is not valid YAML: %v", err)
		}
		decodedProfiles++
	}
	if decodedProfiles != len(filaments) {
		t.Fatalf("decoded YAML profiles = %d, want %d", decodedProfiles, len(filaments))
	}
}

func TestExportUsesVerifiedGenericBasesForCompoundAndAliasMaterials(t *testing.T) {
	for material, base := range map[string]string{
		"PC-CF": "Generic PC-CF", "PP-CF": "Generic PPCF", "NYLON": "Generic PA",
	} {
		t.Run(material, func(t *testing.T) {
			bundle, _, err := profilesync.Export([]profilesync.Filament{{ID: 1, Name: material, Material: material}})
			if err != nil {
				t.Fatalf("Export() error = %v", err)
			}
			profile := string(unzipFiles(t, bundle)[profilesync.ManagedProfilePath])
			if !strings.Contains(profile, `- "`+base+`"`) {
				t.Fatalf("material %q profile does not inherit %q:\n%s", material, base, profile)
			}
		})
	}
}

func TestExportRequiresExplicitBaseForUnknownMaterial(t *testing.T) {
	for _, material := range []string{"ULTEM", "PET"} {
		_, _, err := profilesync.Export([]profilesync.Filament{{ID: 9, Name: "Mystery", Material: material}})
		if err == nil || !strings.Contains(err.Error(), "prusaslicer_base_profile") {
			t.Fatalf("Export(material %q) error = %v, want explicit-base guidance", material, err)
		}
	}

	bundle, _, err := profilesync.Export([]profilesync.Filament{{
		ID: 9, Name: "Mystery", Material: "ULTEM",
		Extra: map[string]string{"prusaslicer_base_profile": "My validated ULTEM base"},
	}})
	if err != nil {
		t.Fatalf("Export(explicit base) error = %v", err)
	}
	if got := string(unzipFiles(t, bundle)[profilesync.ManagedProfilePath]); !strings.Contains(got, `- "My validated ULTEM base"`) {
		t.Fatalf("profile does not use explicit base:\n%s", got)
	}
}

func unzipFiles(t *testing.T, archive []byte) map[string][]byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	files := make(map[string][]byte, len(reader.File))
	for _, file := range reader.File {
		input, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		data, err := io.ReadAll(input)
		_ = input.Close()
		if err != nil {
			t.Fatalf("read %s: %v", file.Name, err)
		}
		files[file.Name] = data
	}
	return files
}

func sortedKeys(values map[string][]byte) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	if len(keys) == 2 && keys[0] > keys[1] {
		keys[0], keys[1] = keys[1], keys[0]
	}
	return keys
}
