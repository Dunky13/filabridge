package main

import (
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"testing"
)

type compatibilityFixture struct {
	Version       string             `json:"version"`
	Format        string             `json:"format"`
	ToolCount     int                `json:"tool_count"`
	Path          string             `json:"path"`
	Metadata      string             `json:"metadata"`
	ExpectedGrams map[string]float64 `json:"expected_grams"`
}

func TestPrusaSlicerCompatibilityMatrix(t *testing.T) {
	manifest, err := os.ReadFile("testdata/compatibility/fixtures.json")
	if err != nil {
		t.Fatalf("read fixture manifest: %v", err)
	}
	var fixtures []compatibilityFixture
	if err := json.Unmarshal(manifest, &fixtures); err != nil {
		t.Fatalf("decode fixture manifest: %v", err)
	}
	versions := make(map[string]bool)
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.Version+"/"+fixture.Format+"/"+fixture.Path, func(t *testing.T) {
			versions[fixture.Version] = true
			var content []byte
			if fixture.Path != "" {
				content, err = os.ReadFile(fixture.Path)
				if err != nil {
					t.Fatalf("read fixture: %v", err)
				}
			} else {
				content = buildBGCodeFixture(t, bgCodeFixtureOptions{
					metadata:    fixture.Metadata,
					compression: bgCodeCompressionDeflate,
					checksum:    bgCodeChecksumCRC32,
				})
			}
			usage, err := NewPrusaLinkClient("printer.invalid", "", 1, 1).ParseGcodeFilamentUsage(content)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			expected := make(map[int]float64, len(fixture.ExpectedGrams))
			for key, weight := range fixture.ExpectedGrams {
				toolID, err := strconv.Atoi(key)
				if err != nil {
					t.Fatalf("invalid expected tool ID %q", key)
				}
				expected[toolID] = weight
			}
			if !reflect.DeepEqual(usage, expected) {
				t.Fatalf("usage = %#v, want %#v", usage, expected)
			}
			for toolID := 0; toolID < fixture.ToolCount; toolID++ {
				if usage[toolID] != expected[toolID] {
					t.Errorf("tool %d weight = %.2f, want %.2f (including explicit unused positions)", toolID, usage[toolID], expected[toolID])
				}
			}
		})
	}
	for _, required := range []string{"2.9.6", "3.0.0-alpha11"} {
		if !versions[required] {
			t.Errorf("compatibility matrix is missing PrusaSlicer %s", required)
		}
	}
}
