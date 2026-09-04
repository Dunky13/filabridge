package profilesync

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const MaxBundleBytes = 8 * 1024 * 1024

// Slicer supplies the installed PrusaSlicer version used for compatibility
// gating. ExecSlicer is the production implementation.
type Slicer interface {
	Version(context.Context) (string, error)
}

type ExecSlicer struct {
	Executable string
}

func (s ExecSlicer) Version(ctx context.Context) (string, error) {
	executable := strings.TrimSpace(s.Executable)
	if executable == "" {
		executable = "prusa-slicer"
	}
	output, err := exec.CommandContext(ctx, executable, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("run %s --version: %w: %s", executable, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

type SyncOptions struct {
	URL               string
	Username          string
	Password          string
	DataDir           string
	AllowInsecureHTTP bool
	HTTPClient        *http.Client
	Slicer            Slicer
}

type SyncResult struct {
	Version string
	Target  string
	Changed bool
}

// Sync downloads, validates, and atomically replaces FilaBridge's single
// managed profile file. It never writes any other user profile.
func Sync(ctx context.Context, options SyncOptions) (SyncResult, error) {
	if strings.TrimSpace(options.DataDir) == "" {
		return SyncResult{}, fmt.Errorf("PrusaSlicer --data-dir is required")
	}
	endpoint, err := url.Parse(options.URL)
	if err != nil || endpoint.Host == "" {
		return SyncResult{}, fmt.Errorf("invalid FilaBridge profile URL %q", options.URL)
	}
	if endpoint.Scheme != "https" {
		if endpoint.Scheme != "http" || !options.AllowInsecureHTTP {
			return SyncResult{}, fmt.Errorf("profile sync requires HTTPS; use --allow-insecure-http only on a trusted network")
		}
	}
	if endpoint.User != nil {
		return SyncResult{}, fmt.Errorf("credentials must not be embedded in profile URL")
	}
	if options.Slicer == nil {
		return SyncResult{}, fmt.Errorf("PrusaSlicer executable is required")
	}
	versionOutput, err := options.Slicer.Version(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	if !isPrusaSlicer3(versionOutput) {
		return SyncResult{}, fmt.Errorf("profile sync requires PrusaSlicer major version 3, got %q", versionOutput)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return SyncResult{}, fmt.Errorf("create profile request: %w", err)
	}
	request.Header.Set("Accept", "application/zip")
	if options.Username != "" || options.Password != "" {
		if options.Username == "" || options.Password == "" {
			return SyncResult{}, fmt.Errorf("both FilaBridge username and password are required")
		}
		request.SetBasicAuth(options.Username, options.Password)
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	client = clientWithSecureRedirects(client)
	response, err := client.Do(request)
	if err != nil {
		return SyncResult{}, fmt.Errorf("download profiles: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message := readErrorMessage(response.Body)
		if message == "" {
			return SyncResult{}, fmt.Errorf("download profiles: HTTP %d", response.StatusCode)
		}
		return SyncResult{}, fmt.Errorf("download profiles: HTTP %d: %s", response.StatusCode, message)
	}
	archive, err := io.ReadAll(io.LimitReader(response.Body, MaxBundleBytes+1))
	if err != nil {
		return SyncResult{}, fmt.Errorf("read profile archive: %w", err)
	}
	if len(archive) > MaxBundleBytes {
		return SyncResult{}, fmt.Errorf("profile archive is too large: maximum is %d bytes", MaxBundleBytes)
	}
	manifest, profile, err := unpack(archive)
	if err != nil {
		return SyncResult{}, err
	}

	target := filepath.Join(options.DataDir, filepath.FromSlash(ManagedProfilePath))
	changed, err := installManagedFile(target, profile)
	if err != nil {
		return SyncResult{}, err
	}
	return SyncResult{Version: manifest.Version, Target: target, Changed: changed}, nil
}

func readErrorMessage(input io.Reader) string {
	const maxErrorBytes = 4096
	data, err := io.ReadAll(io.LimitReader(input, maxErrorBytes+1))
	if err != nil {
		return ""
	}
	truncated := len(data) > maxErrorBytes
	if truncated {
		data = data[:maxErrorBytes]
	}
	var payload struct {
		Error string `json:"error"`
	}
	message := ""
	if json.Unmarshal(data, &payload) == nil {
		message = strings.TrimSpace(payload.Error)
	}
	if message == "" {
		message = strings.TrimSpace(string(data))
	}
	message = strings.Map(func(value rune) rune {
		if unicode.IsControl(value) {
			return ' '
		}
		return value
	}, message)
	if truncated {
		message += "..."
	}
	return message
}

func clientWithSecureRedirects(client *http.Client) *http.Client {
	clone := *client
	configuredPolicy := client.CheckRedirect
	clone.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if err := secureRedirect(request, via); err != nil {
			return err
		}
		if configuredPolicy != nil {
			return configuredPolicy(request, via)
		}
		return nil
	}
	return &clone
}

func secureRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("too many redirects")
	}
	if request.URL.Scheme != "https" {
		return fmt.Errorf("refusing redirect from HTTPS to %s", request.URL.Scheme)
	}
	if len(via) > 0 && !sameHost(via[0].URL, request.URL) {
		request.Header.Del("Authorization")
	}
	return nil
}

func sameHost(first, second *url.URL) bool {
	return strings.EqualFold(first.Hostname(), second.Hostname()) && normalizedPort(first) == normalizedPort(second)
}

func normalizedPort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	if value.Scheme == "https" {
		return "443"
	}
	return "80"
}

var slicerVersionPattern = regexp.MustCompile(`(?i)^\s*(?:PrusaSlicer(?:-|\s+))?v?3\.\d+\.\d+(?:-[0-9a-z.-]+)?(?:\+[0-9a-z.-]+)?\s*$`)

func isPrusaSlicer3(output string) bool {
	return slicerVersionPattern.MatchString(strings.TrimSpace(output))
}

func unpack(archive []byte) (Manifest, []byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("open profile archive: %w", err)
	}
	if len(reader.File) != 2 {
		return Manifest{}, nil, fmt.Errorf("profile archive must contain exactly manifest.json and managed profile")
	}
	files := make(map[string][]byte, 2)
	for _, file := range reader.File {
		if file.Name != "manifest.json" && file.Name != ManagedProfilePath {
			return Manifest{}, nil, fmt.Errorf("unexpected profile archive entry %q", file.Name)
		}
		if _, duplicate := files[file.Name]; duplicate {
			return Manifest{}, nil, fmt.Errorf("duplicate profile archive entry %q", file.Name)
		}
		if file.UncompressedSize64 > MaxBundleBytes {
			return Manifest{}, nil, fmt.Errorf("profile archive entry %q is too large", file.Name)
		}
		input, openErr := file.Open()
		if openErr != nil {
			return Manifest{}, nil, fmt.Errorf("open profile archive entry %q: %w", file.Name, openErr)
		}
		data, readErr := io.ReadAll(io.LimitReader(input, MaxBundleBytes+1))
		closeErr := input.Close()
		if readErr != nil {
			return Manifest{}, nil, fmt.Errorf("read profile archive entry %q: %w", file.Name, readErr)
		}
		if closeErr != nil {
			return Manifest{}, nil, fmt.Errorf("close profile archive entry %q: %w", file.Name, closeErr)
		}
		if len(data) > MaxBundleBytes {
			return Manifest{}, nil, fmt.Errorf("profile archive entry %q is too large", file.Name)
		}
		files[file.Name] = data
	}
	var manifest Manifest
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		return Manifest{}, nil, fmt.Errorf("decode profile manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 || manifest.PrusaSlicerMajor != 3 || manifest.ManagedProfile != ManagedProfilePath || manifest.Version == "" {
		return Manifest{}, nil, fmt.Errorf("unsupported profile manifest")
	}
	if versionBytes, err := hex.DecodeString(manifest.Version); err != nil || len(versionBytes) != 8 {
		return Manifest{}, nil, fmt.Errorf("invalid profile manifest version")
	}
	if checksumBytes, err := hex.DecodeString(manifest.ProfileSHA256); err != nil || len(checksumBytes) != sha256.Size {
		return Manifest{}, nil, fmt.Errorf("invalid managed profile checksum")
	}
	profile := files[ManagedProfilePath]
	if !bytes.HasPrefix(profile, []byte(managedMarker+"\n")) {
		return Manifest{}, nil, fmt.Errorf("managed profile marker is missing")
	}
	hash := sha256.Sum256(profile)
	if !strings.EqualFold(manifest.ProfileSHA256, hex.EncodeToString(hash[:])) {
		return Manifest{}, nil, fmt.Errorf("managed profile checksum mismatch")
	}
	return manifest, profile, nil
}

func installManagedFile(target string, profile []byte) (bool, error) {
	existing, err := os.ReadFile(target)
	if err == nil {
		if !bytes.HasPrefix(existing, []byte(managedMarker+"\n")) {
			return false, fmt.Errorf("refusing to replace %s: file is not owned by FilaBridge", target)
		}
		if bytes.Equal(existing, profile) {
			return false, nil
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("read existing managed profile: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return false, fmt.Errorf("create managed profile directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".filabridge-profile-*.tmp")
	if err != nil {
		return false, fmt.Errorf("create temporary managed profile: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return false, fmt.Errorf("set managed profile permissions: %w", err)
	}
	if _, err := temporary.Write(profile); err != nil {
		_ = temporary.Close()
		return false, fmt.Errorf("write temporary managed profile: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return false, fmt.Errorf("sync temporary managed profile: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return false, fmt.Errorf("close temporary managed profile: %w", err)
	}
	// os.Rename replaces atomically on Unix. Go maps it to
	// MOVEFILE_REPLACE_EXISTING on Windows, so managed-profile updates retain
	// their old destination if replacement fails.
	if err := os.Rename(temporaryName, target); err != nil {
		return false, fmt.Errorf("atomically install managed profile: %w", err)
	}
	return true, nil
}
