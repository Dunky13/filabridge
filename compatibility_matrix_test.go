package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var rawFilamentGramsLine = regexp.MustCompile(`(?m)^;\s*filament used \[g\]\s*=\s*([0-9.,[:space:]]+)$`)

type compatibilityFixture struct {
	Version       string             `json:"version"`
	Format        string             `json:"format"`
	ToolCount     int                `json:"tool_count"`
	Path          string             `json:"path"`
	Metadata      string             `json:"metadata"`
	ExpectedGrams map[string]float64 `json:"expected_grams"`
}

type goldenCompatibilityManifest struct {
	PrusaSlicerArtifacts []goldenCompatibilityArtifact `json:"prusaslicer_artifacts"`
}

type goldenCompatibilityArtifact struct {
	ID                         string             `json:"id"`
	ArtifactPath               string             `json:"artifact_path"`
	SHA256                     string             `json:"sha256"`
	Format                     string             `json:"format"`
	Toolheads                  int                `json:"toolheads"`
	UsedToolheads              int                `json:"used_toolheads"`
	ExpectedGramsByLogicalTool map[string]float64 `json:"expected_grams_by_logical_tool"`
	Production                 struct {
		Producer        string `json:"producer"`
		ProducerCommit  string `json:"producer_commit"`
		InputArtifactID string `json:"input_artifact_id"`
	} `json:"production"`
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

func TestPrusaSlicerGoldenCompatibilityMatrix(t *testing.T) {
	manifestBytes, err := os.ReadFile("testdata/compatibility/golden-fixtures.json")
	if err != nil {
		t.Fatalf("read golden fixture manifest: %v", err)
	}
	var manifest goldenCompatibilityManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode golden fixture manifest: %v", err)
	}
	if len(manifest.PrusaSlicerArtifacts) == 0 {
		t.Fatal("golden fixture manifest has no real PrusaSlicer artifacts")
	}
	artifactsByID := make(map[string]goldenCompatibilityArtifact, len(manifest.PrusaSlicerArtifacts))
	for _, artifact := range manifest.PrusaSlicerArtifacts {
		artifactsByID[artifact.ID] = artifact
	}

	for _, artifact := range manifest.PrusaSlicerArtifacts {
		artifact := artifact
		t.Run(artifact.ID, func(t *testing.T) {
			content, err := os.ReadFile(artifact.ArtifactPath)
			if err != nil {
				t.Fatalf("read golden artifact: %v", err)
			}
			if checksum := fmt.Sprintf("%x", sha256.Sum256(content)); checksum != artifact.SHA256 {
				t.Fatalf("SHA-256 = %s, want %s", checksum, artifact.SHA256)
			}
			if len(artifact.ExpectedGramsByLogicalTool) != artifact.Toolheads {
				t.Fatalf("logical-tool vector has %d entries, want exactly %d", len(artifact.ExpectedGramsByLogicalTool), artifact.Toolheads)
			}
			if artifact.Format == "gcode" {
				assertRawFilamentVector(t, content, artifact)
			}

			usage, err := NewPrusaLinkClient("printer.invalid", "", 1, 1).ParseGcodeFilamentUsage(content)
			if err != nil {
				t.Fatalf("parse golden artifact: %v", err)
			}
			expectedUsed := make(map[int]float64)
			for rawToolID, grams := range artifact.ExpectedGramsByLogicalTool {
				toolID, err := strconv.Atoi(rawToolID)
				if err != nil || toolID < 0 || toolID >= artifact.Toolheads {
					t.Fatalf("invalid logical tool %q for %d-tool artifact", rawToolID, artifact.Toolheads)
				}
				if grams > 0 {
					expectedUsed[toolID] = grams
				}
			}
			if !reflect.DeepEqual(usage, expectedUsed) {
				t.Fatalf("usage = %#v, want %#v", usage, expectedUsed)
			}
			if len(expectedUsed) != artifact.UsedToolheads {
				t.Fatalf("used logical tools = %d, manifest claims %d", len(expectedUsed), artifact.UsedToolheads)
			}
			for toolID := 0; toolID < artifact.Toolheads; toolID++ {
				if _, ok := artifact.ExpectedGramsByLogicalTool[strconv.Itoa(toolID)]; !ok {
					t.Fatalf("logical-tool vector is missing position %d", toolID)
				}
			}

			switch artifact.Production.Producer {
			case "prusaslicer":
				if artifact.Production.InputArtifactID != "" {
					t.Fatalf("PrusaSlicer production lineage is inconsistent: %#v", artifact.Production)
				}
			case "libbgcode":
				input, ok := artifactsByID[artifact.Production.InputArtifactID]
				if artifact.Format != "bgcode" || !ok || input.Format != "gcode" || input.Production.Producer != "prusaslicer" {
					t.Fatalf("libbgcode production lineage is inconsistent: %#v", artifact.Production)
				}
			default:
				t.Fatalf("unknown artifact producer %q", artifact.Production.Producer)
			}
		})
	}
}

func assertRawFilamentVector(t *testing.T, content []byte, artifact goldenCompatibilityArtifact) {
	t.Helper()
	matches := rawFilamentGramsLine.FindAllSubmatch(content, -1)
	if len(matches) == 0 {
		t.Fatal("raw G-code has no exact filament-used grams metadata line")
	}
	rawValues := strings.Split(strings.TrimSpace(string(matches[len(matches)-1][1])), ",")
	if len(rawValues) != artifact.Toolheads {
		t.Fatalf("raw G-code filament vector has %d positions, want %d", len(rawValues), artifact.Toolheads)
	}
	positive := 0
	for toolID, rawValue := range rawValues {
		grams, err := strconv.ParseFloat(strings.TrimSpace(rawValue), 64)
		if err != nil {
			t.Fatalf("parse raw grams for logical tool %d: %v", toolID, err)
		}
		want, ok := artifact.ExpectedGramsByLogicalTool[strconv.Itoa(toolID)]
		if !ok || grams != want {
			t.Fatalf("raw logical tool %d grams = %.2f, manifest = %.2f (present=%t)", toolID, grams, want, ok)
		}
		if grams > 0 {
			positive++
		}
	}
	if positive != artifact.UsedToolheads {
		t.Fatalf("raw G-code uses %d logical tools, manifest claims %d", positive, artifact.UsedToolheads)
	}
}
