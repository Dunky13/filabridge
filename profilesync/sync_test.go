package profilesync_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filabridge/profilesync"
)

type fakeSlicer struct {
	version string
	calls   int
}

func (s *fakeSlicer) Version(context.Context) (string, error) {
	s.calls++
	return s.version, nil
}

func TestSyncDownloadsAuthenticatedArchiveAndAtomicallyInstallsManagedProfile(t *testing.T) {
	bundle, manifest, err := profilesync.Export([]profilesync.Filament{{ID: 12, Name: "Orange PLA", Material: "PLA", ColorHex: "ff6600"}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "operator" || password != "secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(bundle)
	}))
	defer server.Close()

	dataDir := t.TempDir()
	slicer := &fakeSlicer{version: "PrusaSlicer 3.0.0-alpha11"}
	result, err := profilesync.Sync(context.Background(), profilesync.SyncOptions{
		URL: server.URL, Username: "operator", Password: "secret", DataDir: dataDir,
		HTTPClient: server.Client(), Slicer: slicer,
	})
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if result.Version != manifest.Version || !result.Changed || slicer.calls != 1 {
		t.Fatalf("Sync() result = %#v, slicer calls = %d", result, slicer.calls)
	}
	written, err := os.ReadFile(filepath.Join(dataDir, filepath.FromSlash(profilesync.ManagedProfilePath)))
	if err != nil {
		t.Fatalf("read installed profile: %v", err)
	}
	if !strings.Contains(string(written), `id: "filabridge:spoolman-filament:12"`) {
		t.Fatalf("installed profile unexpected:\n%s", written)
	}
	temporaryFiles, err := filepath.Glob(filepath.Join(filepath.Dir(filepath.Join(dataDir, filepath.FromSlash(profilesync.ManagedProfilePath))), ".filabridge-profile-*.tmp"))
	if err != nil || len(temporaryFiles) != 0 {
		t.Fatalf("temporary profile files after sync = %v, err=%v", temporaryFiles, err)
	}

	result, err = profilesync.Sync(context.Background(), profilesync.SyncOptions{
		URL: server.URL, Username: "operator", Password: "secret", DataDir: dataDir,
		HTTPClient: server.Client(), Slicer: slicer,
	})
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if result.Changed {
		t.Fatal("unchanged second sync reported a change")
	}

	replacementBundle, replacementManifest, err := profilesync.Export([]profilesync.Filament{{ID: 13, Name: "Blue PETG", Material: "PETG"}})
	if err != nil {
		t.Fatal(err)
	}
	replacementServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(replacementBundle) }))
	defer replacementServer.Close()
	result, err = profilesync.Sync(context.Background(), profilesync.SyncOptions{
		URL: replacementServer.URL, DataDir: dataDir,
		HTTPClient: replacementServer.Client(), Slicer: slicer,
	})
	if err != nil {
		t.Fatalf("replacement Sync() error = %v", err)
	}
	if !result.Changed || result.Version != replacementManifest.Version {
		t.Fatalf("replacement Sync() result = %#v", result)
	}
	written, err = os.ReadFile(filepath.Join(dataDir, filepath.FromSlash(profilesync.ManagedProfilePath)))
	if err != nil || !strings.Contains(string(written), `id: "filabridge:spoolman-filament:13"`) {
		t.Fatalf("managed profile was not replaced safely: %q, err=%v", written, err)
	}
}

func TestSyncRefusesUnsupportedSlicerAndUserOwnedCollision(t *testing.T) {
	bundle, _, err := profilesync.Export([]profilesync.Filament{{ID: 1, Name: "PLA", Material: "PLA"}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(bundle) }))
	defer server.Close()

	dataDir := t.TempDir()
	_, err = profilesync.Sync(context.Background(), profilesync.SyncOptions{
		URL: server.URL, AllowInsecureHTTP: true, DataDir: dataDir,
		Slicer: &fakeSlicer{version: "PrusaSlicer 2.9.4"},
	})
	if err == nil || !strings.Contains(err.Error(), "major version 3") {
		t.Fatalf("Sync(PrusaSlicer 2) error = %v", err)
	}

	target := filepath.Join(dataDir, filepath.FromSlash(profilesync.ManagedProfilePath))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	const userProfile = "kind: filament\nname: My profile\n"
	if err := os.WriteFile(target, []byte(userProfile), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = profilesync.Sync(context.Background(), profilesync.SyncOptions{
		URL: server.URL, AllowInsecureHTTP: true, DataDir: dataDir,
		Slicer: &fakeSlicer{version: "PrusaSlicer 3.0.0-alpha11"},
	})
	if err == nil || !strings.Contains(err.Error(), "not owned by FilaBridge") {
		t.Fatalf("Sync(user collision) error = %v", err)
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil || string(got) != userProfile {
		t.Fatalf("user file changed: %q, err=%v", got, readErr)
	}
}

func TestSyncRejectsNonLoopbackPlainHTTPByDefaultAndOversizedDownload(t *testing.T) {
	slicer := &fakeSlicer{version: "PrusaSlicer 3.0.0"}
	_, err := profilesync.Sync(context.Background(), profilesync.SyncOptions{
		URL: "http://filabridge.example/api/prusaslicer/profiles.zip", DataDir: t.TempDir(), Slicer: slicer,
	})
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("Sync(plain HTTP) error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, profilesync.MaxBundleBytes+1))
	}))
	defer server.Close()
	_, err = profilesync.Sync(context.Background(), profilesync.SyncOptions{
		URL: server.URL, AllowInsecureHTTP: true, DataDir: t.TempDir(), Slicer: slicer,
	})
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("Sync(oversized) error = %v", err)
	}
}

func TestSyncSurfacesBoundedServerValidationError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"filament 9 needs extra.prusaslicer_base_profile"}`))
	}))
	defer server.Close()
	_, err := profilesync.Sync(context.Background(), profilesync.SyncOptions{
		URL: server.URL, AllowInsecureHTTP: true, DataDir: t.TempDir(), Slicer: &fakeSlicer{version: "PrusaSlicer 3.0.0"},
	})
	if err == nil || !strings.Contains(err.Error(), "extra.prusaslicer_base_profile") {
		t.Fatalf("Sync(server validation error) = %v", err)
	}
}

func TestSyncRejectsTLSdowngradeWithCallerSuppliedHTTPClient(t *testing.T) {
	authorizationAtPlaintextServer := ""
	plaintext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizationAtPlaintextServer = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("not reached"))
	}))
	defer plaintext.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plaintext.URL, http.StatusFound)
	}))
	defer secure.Close()

	_, err := profilesync.Sync(context.Background(), profilesync.SyncOptions{
		URL: secure.URL, Username: "operator", Password: "secret", DataDir: t.TempDir(),
		HTTPClient: secure.Client(), Slicer: &fakeSlicer{version: "PrusaSlicer 3.0.0"},
	})
	if err == nil || !strings.Contains(err.Error(), "refusing redirect") {
		t.Fatalf("Sync(TLS downgrade) error = %v", err)
	}
	if authorizationAtPlaintextServer != "" {
		t.Fatalf("Authorization reached plaintext redirect target: %q", authorizationAtPlaintextServer)
	}
}

func TestSyncAcceptsOfficialPrusaSlicer3VersionShapes(t *testing.T) {
	bundle, _, err := profilesync.Export([]profilesync.Filament{{ID: 1, Name: "PLA", Material: "PLA"}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(bundle) }))
	defer server.Close()

	for _, version := range []string{"3.0.0-alpha11", "PrusaSlicer-3.0.0-alpha11", "PrusaSlicer 3.1.2+build.5"} {
		t.Run(version, func(t *testing.T) {
			_, err := profilesync.Sync(context.Background(), profilesync.SyncOptions{
				URL: server.URL, AllowInsecureHTTP: true, DataDir: t.TempDir(), Slicer: &fakeSlicer{version: version},
			})
			if err != nil {
				t.Fatalf("Sync(version %q) error = %v", version, err)
			}
		})
	}
	for _, version := range []string{"other tool 3.0.0", "PrusaSlicer 2.9.6", "PrusaSlicer 30.0.0"} {
		t.Run("reject "+version, func(t *testing.T) {
			_, err := profilesync.Sync(context.Background(), profilesync.SyncOptions{
				URL: server.URL, AllowInsecureHTTP: true, DataDir: t.TempDir(), Slicer: &fakeSlicer{version: version},
			})
			if err == nil || !strings.Contains(err.Error(), "major version 3") {
				t.Fatalf("Sync(version %q) error = %v", version, err)
			}
		})
	}
}
