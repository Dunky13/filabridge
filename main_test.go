package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"filabridge/profilesync"
)

type mainFakeSlicer struct{ version string }

func (s *mainFakeSlicer) Version(context.Context) (string, error) { return s.version, nil }

func TestRunRejectsConflictingModesBeforeStartup(t *testing.T) {
	err := run(context.Background(), []string{"--web-only", "--bridge-only"})
	if err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("run() error = %v, want conflicting mode error", err)
	}
}

func TestProfileSyncSubcommandUsesReleaseBinaryAndExplicitDataDirectory(t *testing.T) {
	bundle, manifest, err := profilesync.Export([]profilesync.Filament{{ID: 4, Name: "PLA", Material: "PLA"}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, _ := r.BasicAuth()
		if username != "operator" || password != "secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(bundle)
	}))
	defer server.Close()
	t.Setenv("FILABRIDGE_SYNC_USERNAME", "operator")
	t.Setenv("FILABRIDGE_SYNC_PASSWORD", "secret")

	dataDir := t.TempDir()
	var stdout bytes.Buffer
	slicer := &mainFakeSlicer{version: "PrusaSlicer 3.0.0-alpha11"}
	err = runProfileSync(context.Background(), []string{
		"--url", server.URL,
		"--data-dir", dataDir,
	}, &stdout, server.Client(), slicer)
	if err != nil {
		t.Fatalf("profile-sync error = %v", err)
	}
	if output := stdout.String(); !strings.Contains(output, "updated") || !strings.Contains(output, manifest.Version) {
		t.Fatalf("profile-sync output = %q", output)
	}
}

func TestProfileSyncSubcommandRequiresExplicitDataDirectory(t *testing.T) {
	var stdout bytes.Buffer
	err := runProfileSync(context.Background(), []string{"--url", "https://filabridge.example/profiles.zip"}, &stdout, nil, &mainFakeSlicer{version: "PrusaSlicer 3.0.0"})
	if err == nil || !strings.Contains(err.Error(), "--data-dir") {
		t.Fatalf("profile-sync error = %v, want --data-dir guidance", err)
	}
}

func TestProfileSyncSubcommandPrintsHelpSuccessfully(t *testing.T) {
	var stdout bytes.Buffer
	err := runProfileSync(context.Background(), []string{"--help"}, &stdout, nil, nil)
	if err != nil {
		t.Fatalf("profile-sync --help error = %v", err)
	}
	if output := stdout.String(); !strings.Contains(output, "-data-dir") || !strings.Contains(output, "-url") {
		t.Fatalf("profile-sync --help output = %q", output)
	}
}
