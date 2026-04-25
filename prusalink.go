package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// PrusaLinkClient handles communication with PrusaLink API
type PrusaLinkClient struct {
	baseURL             string
	apiKey              string
	httpClient          *http.Client
	fileDownloadTimeout int
}

const prusaLinkDownloadSniffBytes = 256 * 1024

// PrusaLinkStatus represents the status response from PrusaLink
type PrusaLinkStatus struct {
	Job struct {
		ID            int     `json:"id"`
		Progress      float64 `json:"progress"`
		TimeRemaining int     `json:"time_remaining"`
		TimePrinting  int     `json:"time_printing"`
	} `json:"job"`
	Printer struct {
		State string `json:"state"`
	} `json:"printer"`
}

// PrusaLinkJob represents the job response from PrusaLink
type PrusaLinkJob struct {
	ID            int     `json:"id"`
	State         string  `json:"state"`
	Progress      float64 `json:"progress"`
	TimeRemaining int     `json:"time_remaining"`
	TimePrinting  int     `json:"time_printing"`
	File          struct {
		Name        string                     `json:"name"`
		DisplayName string                     `json:"display_name"`
		Path        string                     `json:"path"`
		Size        int                        `json:"size"`
		Meta        PrusaLinkPrintFileMetadata `json:"meta"`
		Refs        struct {
			Download string `json:"download"`
		} `json:"refs"`
	} `json:"file"`
}

type PrusaLinkPrintFileMetadata struct {
	FilamentUsedG              float64   `json:"filament_used_g"`
	LegacyFilamentUsedG        float64   `json:"filament used [g]"`
	FilamentUsedGPerTool       []float64 `json:"filament_used_g_per_tool"`
	LegacyFilamentUsedGPerTool []float64 `json:"filament used [g] per tool"`
}

type PrusaLinkPrintFileInfo struct {
	Name        string                     `json:"name"`
	DisplayName string                     `json:"display_name"`
	Path        string                     `json:"path"`
	Meta        PrusaLinkPrintFileMetadata `json:"meta"`
	Refs        struct {
		Download string `json:"download"`
	} `json:"refs"`
}

type PrusaLinkStorageResponse struct {
	StorageList []PrusaLinkStorage `json:"storage_list"`
}

type PrusaLinkStorage struct {
	Path      string `json:"path"`
	Available bool   `json:"available"`
	Type      string `json:"type"`
}

// FilamentUsageByToolhead converts print file metadata into toolhead-weight mapping.
func (m PrusaLinkPrintFileMetadata) FilamentUsageByToolhead() map[int]float64 {
	perTool := m.FilamentUsedGPerTool
	if len(perTool) == 0 {
		perTool = m.LegacyFilamentUsedGPerTool
	}

	if len(perTool) > 0 {
		usage := make(map[int]float64, len(perTool))
		for toolheadID, weight := range perTool {
			if weight <= 0 {
				continue
			}
			usage[toolheadID] = weight
		}
		if len(usage) > 0 {
			return usage
		}
	}

	totalWeight := m.FilamentUsedG
	if totalWeight <= 0 {
		totalWeight = m.LegacyFilamentUsedG
	}

	if totalWeight > 0 {
		return map[int]float64{0: totalWeight}
	}

	return nil
}

// FilamentUsageByToolhead converts job file metadata into toolhead-weight mapping.
func (j *PrusaLinkJob) FilamentUsageByToolhead() map[int]float64 {
	if j == nil {
		return nil
	}

	return j.File.Meta.FilamentUsageByToolhead()
}

// PrusaLinkInfo represents the printer info response from PrusaLink
type PrusaLinkInfo struct {
	Hostname         string  `json:"hostname"`
	Serial           string  `json:"serial"`
	NozzleDiameter   float64 `json:"nozzle_diameter"`
	MMU              bool    `json:"mmu"`
	MinExtrusionTemp int     `json:"min_extrusion_temp"`
}

// NewPrusaLinkClient creates a new PrusaLink client
func NewPrusaLinkClient(ipAddress, apiKey string, timeout, fileDownloadTimeout int) *PrusaLinkClient {
	// Create a custom dialer with timeout for DNS resolution
	// This ensures hostnames (especially .local domains) have adequate time to resolve
	dialer := &net.Dialer{
		Timeout:   5 * time.Second, // DNS resolution timeout
		KeepAlive: 30 * time.Second,
	}

	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second, // Timeout for receiving response headers
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &PrusaLinkClient{
		baseURL:             fmt.Sprintf("http://%s", ipAddress),
		apiKey:              apiKey,
		fileDownloadTimeout: fileDownloadTimeout,
		httpClient: &http.Client{
			Timeout:   time.Duration(timeout) * time.Second,
			Transport: transport,
		},
	}
}

// addAPIKey adds API key authentication to the request
func (c *PrusaLinkClient) addAPIKey(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("X-Api-Key", c.apiKey)
	}
}

// GetStatus retrieves the current status of the printer
func (c *PrusaLinkClient) GetStatus() (*PrusaLinkStatus, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/status", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create status request: %w", err)
	}

	// Add API key authentication
	c.addAPIKey(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get status from PrusaLink: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("PrusaLink API error: %d - %s", resp.StatusCode, string(body))
	}

	var status PrusaLinkStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("failed to decode status response: %w", err)
	}

	return &status, nil
}

// GetJobInfo retrieves the current job information
func (c *PrusaLinkClient) GetJobInfo() (*PrusaLinkJob, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/job", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create job request: %w", err)
	}

	// Add API key authentication
	c.addAPIKey(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get job info from PrusaLink: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("PrusaLink API error: %d - %s", resp.StatusCode, string(body))
	}

	// Handle 204 No Content (no active job)
	if resp.StatusCode == http.StatusNoContent {
		return &PrusaLinkJob{}, nil
	}

	var job PrusaLinkJob
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, fmt.Errorf("failed to decode job response: %w", err)
	}

	return &job, nil
}

func (c *PrusaLinkClient) GetPrintFileInfo(storagePath string) (*PrusaLinkPrintFileInfo, error) {
	metadataURL, err := c.buildFileMetadataURL(storagePath)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("GET", metadataURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create file metadata request: %w", err)
	}

	c.addAPIKey(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get print file info from PrusaLink: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("PrusaLink API error: %d - %s", resp.StatusCode, string(body))
	}

	var info PrusaLinkPrintFileInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("failed to decode print file info response: %w", err)
	}

	return &info, nil
}

func (c *PrusaLinkClient) GetPrintFileInfoWithRetry(storagePath string) (*PrusaLinkPrintFileInfo, error) {
	const maxRetries = 3
	backoffDelays := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		log.Printf("Fetching print file metadata attempt %d/%d: %s", attempt+1, maxRetries, storagePath)

		info, err := c.GetPrintFileInfo(storagePath)
		if err == nil {
			return info, nil
		}

		lastErr = err
		log.Printf("Metadata attempt %d failed: %v", attempt+1, lastErr)
		if attempt < maxRetries-1 {
			time.Sleep(backoffDelays[attempt])
		}
	}

	return nil, fmt.Errorf("failed to fetch print file metadata after %d attempts: %w", maxRetries, lastErr)
}

func (c *PrusaLinkClient) GetStoragePaths() ([]string, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/storage", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage request: %w", err)
	}

	c.addAPIKey(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get storage info from PrusaLink: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("PrusaLink API error: %d - %s", resp.StatusCode, string(body))
	}

	var storageResponse PrusaLinkStorageResponse
	if err := json.NewDecoder(resp.Body).Decode(&storageResponse); err != nil {
		return nil, fmt.Errorf("failed to decode storage response: %w", err)
	}

	paths := make([]string, 0, len(storageResponse.StorageList))
	for _, storage := range storageResponse.StorageList {
		if !storage.Available {
			continue
		}
		path := strings.Trim(strings.TrimSpace(storage.Path), "/")
		if path != "" {
			paths = append(paths, path)
		}
	}

	return paths, nil
}

// GetFilamentUsageForFile retrieves filament usage from PrusaLink metadata, then falls back
// to downloading and parsing the print file when metadata is missing or unavailable.
func (c *PrusaLinkClient) GetFilamentUsageForFile(storagePath string) (map[int]float64, error) {
	info, metadataErr := c.GetPrintFileInfoWithRetry(storagePath)
	if metadataErr == nil {
		if usage := info.Meta.FilamentUsageByToolhead(); len(usage) > 0 {
			return usage, nil
		}
		log.Printf("No filament usage in PrusaLink metadata for %s, falling back to file parse", storagePath)
	} else {
		log.Printf("Metadata fetch failed for %s, falling back to file parse: %v", storagePath, metadataErr)
	}

	downloadRef := ""
	if info != nil {
		downloadRef = info.Refs.Download
	}

	filamentUsage, downloadErr := c.GetFilamentUsageFromDownloadWithRetry(storagePath, downloadRef, c.fileDownloadTimeout)
	if downloadErr != nil {
		if metadataErr != nil {
			return nil, fmt.Errorf("metadata fetch failed: %w; file download fallback failed: %v", metadataErr, downloadErr)
		}
		return nil, fmt.Errorf("failed to download print file for filament usage fallback: %w", downloadErr)
	}
	if len(filamentUsage) == 0 {
		if metadataErr != nil {
			return nil, fmt.Errorf("metadata fetch failed: %w; file parse fallback found no filament usage", metadataErr)
		}
		return nil, fmt.Errorf("no filament usage data found in PrusaLink metadata or print file")
	}

	return filamentUsage, nil
}

func (c *PrusaLinkClient) FindStoragePathForJobName(jobName string) (string, error) {
	jobName = strings.Trim(strings.TrimSpace(jobName), "/")
	if jobName == "" {
		return "", fmt.Errorf("job name is empty")
	}

	tryPaths := make([]string, 0, 4)
	seen := make(map[string]struct{})
	addTryPath := func(candidate string) {
		candidate = strings.Trim(strings.TrimSpace(candidate), "/")
		if candidate == "" {
			return
		}
		if _, exists := seen[candidate]; exists {
			return
		}
		seen[candidate] = struct{}{}
		tryPaths = append(tryPaths, candidate)
	}

	if strings.Count(jobName, "/") >= 1 {
		addTryPath(jobName)
	}

	storagePaths, err := c.GetStoragePaths()
	if err != nil {
		return "", err
	}

	for _, storagePath := range storagePaths {
		addTryPath(joinPrusaStoragePath(storagePath, jobName))
		addTryPath(joinPrusaStoragePath(storagePath, prusaPathBase(jobName)))
	}

	var lastErr error
	for _, candidate := range tryPaths {
		_, err := c.GetPrintFileInfo(candidate)
		if err == nil {
			return candidate, nil
		}
		lastErr = err
	}

	if lastErr != nil {
		return "", fmt.Errorf("failed to resolve printer file for %s: %w", jobName, lastErr)
	}

	return "", fmt.Errorf("failed to resolve printer file for %s", jobName)
}

// GetPrinterInfo retrieves the printer information
func (c *PrusaLinkClient) GetPrinterInfo() (*PrusaLinkInfo, error) {
	log.Printf("🔍 [PrusaLink] Getting printer info from %s", c.baseURL)

	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/info", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create printer info request: %w", err)
	}

	// Add API key authentication
	c.addAPIKey(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("❌ [PrusaLink] API call failed for %s: %v", c.baseURL, err)
		return nil, fmt.Errorf("failed to get printer info from PrusaLink: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("❌ [PrusaLink] API error for %s: %d - %s", c.baseURL, resp.StatusCode, string(body))
		return nil, fmt.Errorf("PrusaLink API error: %d - %s", resp.StatusCode, string(body))
	}

	// Read the raw response body for logging
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("❌ [PrusaLink] Failed to read response body from %s: %v", c.baseURL, err)
		return nil, fmt.Errorf("failed to read printer info response: %w", err)
	}

	log.Printf("📥 [PrusaLink] Raw API response from %s: %s", c.baseURL, string(body))

	var info PrusaLinkInfo
	if err := json.Unmarshal(body, &info); err != nil {
		log.Printf("❌ [PrusaLink] JSON unmarshal failed for %s: %v", c.baseURL, err)
		return nil, fmt.Errorf("failed to decode printer info response: %w", err)
	}

	log.Printf("✅ [PrusaLink] Parsed printer info from %s: hostname='%s', serial='%s', nozzle_diameter=%.2f, mmu=%v",
		c.baseURL, info.Hostname, info.Serial, info.NozzleDiameter, info.MMU)

	return &info, nil
}

// GetGcodeFile downloads the G-code file for a completed print job
func (c *PrusaLinkClient) GetGcodeFile(filename string) ([]byte, error) {
	// Use the correct PrusaLink API format: /{filename}
	// The filename should already include the full path (e.g., "usb/SHAPE-~1.BGC")
	req, err := http.NewRequest("GET", c.baseURL+"/"+filename, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create G-code request: %w", err)
	}

	// Add API key authentication
	c.addAPIKey(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get G-code file from PrusaLink: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("PrusaLink API error: %d - %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read G-code file: %w", err)
	}

	return body, nil
}

// GetFilamentUsageFromDownloadWithRetry downloads only as much of the print file as needed
// to extract the "filament used [g]" line, avoiding full bgcode transfers on flaky links.
func (c *PrusaLinkClient) GetFilamentUsageFromDownloadWithRetry(storagePath string, downloadRef string, fileDownloadTimeout int) (map[int]float64, error) {
	const maxRetries = 3
	backoffDelays := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

	var lastErr error
	candidates := c.buildFileDownloadURLCandidates(storagePath, downloadRef)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no download URL candidates available for %s", storagePath)
	}

	for attempt := 0; attempt < maxRetries; attempt++ {
		log.Printf("Downloading G-code file attempt %d/%d: %s", attempt+1, maxRetries, storagePath)

		fileDialer := &net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}

		fileClient := &http.Client{
			Timeout: time.Duration(fileDownloadTimeout) * time.Second,
			Transport: &http.Transport{
				DialContext:           fileDialer.DialContext,
				MaxIdleConns:          10,
				MaxIdleConnsPerHost:   2,
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		}

		log.Printf("File download client configured with %v timeout", fileClient.Timeout)

		for _, candidateURL := range candidates {
			filamentUsage, err := c.tryDownloadFilamentUsage(fileClient, candidateURL, true)
			if err == nil && len(filamentUsage) > 0 {
				log.Printf("Successfully extracted filament usage on attempt %d: %s -> %+v",
					attempt+1, candidateURL, filamentUsage)
				return filamentUsage, nil
			}
			if err != nil {
				lastErr = err
				log.Printf("Attempt %d candidate failed: %v", attempt+1, lastErr)
				continue
			}

			// Range request succeeded but did not include the metadata line. Fall back to full stream parse.
			filamentUsage, err = c.tryDownloadFilamentUsage(fileClient, candidateURL, false)
			if err == nil && len(filamentUsage) > 0 {
				log.Printf("Successfully extracted filament usage on attempt %d with full download: %s -> %+v",
					attempt+1, candidateURL, filamentUsage)
				return filamentUsage, nil
			}
			if err != nil {
				lastErr = err
				log.Printf("Attempt %d full-download candidate failed: %v", attempt+1, lastErr)
				continue
			}

			lastErr = fmt.Errorf("no filament usage found in download stream from %s", candidateURL)
		}

		if attempt < maxRetries-1 {
			time.Sleep(backoffDelays[attempt])
		}
	}

	return nil, fmt.Errorf("failed to download G-code file after %d attempts: %w", maxRetries, lastErr)
}

// GetGcodeFileWithRetry downloads the G-code file with retry logic and exponential backoff
func (c *PrusaLinkClient) GetGcodeFileWithRetry(storagePath string, downloadRef string, fileDownloadTimeout int) ([]byte, error) {
	const maxRetries = 3
	backoffDelays := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

	var lastErr error
	candidates := c.buildFileDownloadURLCandidates(storagePath, downloadRef)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no download URL candidates available for %s", storagePath)
	}

	for attempt := 0; attempt < maxRetries; attempt++ {
		log.Printf("Downloading G-code file attempt %d/%d: %s", attempt+1, maxRetries, storagePath)

		// Create a new client with extended timeout for file downloads
		// Use the same DNS timeout configuration for consistency
		fileDialer := &net.Dialer{
			Timeout:   5 * time.Second, // DNS resolution timeout
			KeepAlive: 30 * time.Second,
		}

		fileClient := &http.Client{
			Timeout: time.Duration(fileDownloadTimeout) * time.Second,
			Transport: &http.Transport{
				DialContext:           fileDialer.DialContext,
				MaxIdleConns:          10,
				MaxIdleConnsPerHost:   2,
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		}

		// Add diagnostic logging to verify timeout values
		log.Printf("File download client configured with %v timeout", fileClient.Timeout)

		for _, candidateURL := range candidates {
			log.Printf("Attempting G-code download from %s", candidateURL)

			req, err := http.NewRequest("GET", candidateURL, nil)
			if err != nil {
				lastErr = fmt.Errorf("failed to create G-code request: %w", err)
				log.Printf("Attempt %d candidate failed: %v", attempt+1, lastErr)
				continue
			}

			// Add API key authentication
			c.addAPIKey(req)

			resp, err := fileClient.Do(req)
			if err != nil {
				lastErr = fmt.Errorf("failed to get G-code file from PrusaLink: %w", err)
				log.Printf("Attempt %d candidate failed: %v", attempt+1, lastErr)
				continue
			}

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				lastErr = fmt.Errorf("PrusaLink API error: %d - %s", resp.StatusCode, string(body))
				log.Printf("Attempt %d candidate failed: %v", attempt+1, lastErr)
				continue
			}

			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				lastErr = fmt.Errorf("failed to read G-code file: %w", err)
				log.Printf("Attempt %d candidate failed: %v", attempt+1, lastErr)
				continue
			}

			// Success!
			log.Printf("Successfully downloaded G-code file on attempt %d: %s (%d bytes)",
				attempt+1, candidateURL, len(body))
			return body, nil
		}

		if attempt < maxRetries-1 {
			time.Sleep(backoffDelays[attempt])
		}
	}

	return nil, fmt.Errorf("failed to download G-code file after %d attempts: %w", maxRetries, lastErr)
}

// ParseGcodeFilamentUsage extracts filament usage from .gcode or .bgcode content
func (c *PrusaLinkClient) ParseGcodeFilamentUsage(gcodeContent []byte) (map[int]float64, error) {
	return c.ParseGcodeFilamentUsageFromReader(bytes.NewReader(gcodeContent))
}

// TestConnection tests the connection to PrusaLink
func (c *PrusaLinkClient) TestConnection() error {
	_, err := c.GetStatus()
	return err
}

func (c *PrusaLinkClient) buildFileMetadataURL(storagePath string) (string, error) {
	storagePath = strings.TrimPrefix(strings.TrimSpace(storagePath), "/")
	if storagePath == "" {
		return "", fmt.Errorf("storage path is empty")
	}

	parts := strings.Split(storagePath, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid storage path: %s", storagePath)
	}

	storage := url.PathEscape(parts[0])
	pathParts := make([]string, 0, len(parts)-1)
	for _, part := range parts[1:] {
		if part == "" {
			continue
		}
		pathParts = append(pathParts, url.PathEscape(part))
	}
	if len(pathParts) == 0 {
		return "", fmt.Errorf("invalid storage path: %s", storagePath)
	}

	return fmt.Sprintf("%s/api/v1/files/%s/%s", c.baseURL, storage, strings.Join(pathParts, "/")), nil
}

func (c *PrusaLinkClient) buildFileDownloadURLCandidates(storagePath string, downloadRef string) []string {
	candidates := make([]string, 0, 3)
	seen := make(map[string]struct{})

	addCandidate := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}

		candidateURL := value
		if !strings.HasPrefix(candidateURL, "http://") && !strings.HasPrefix(candidateURL, "https://") {
			candidateURL = c.baseURL + "/" + strings.TrimPrefix(candidateURL, "/")
		}
		if _, exists := seen[candidateURL]; exists {
			return
		}
		seen[candidateURL] = struct{}{}
		candidates = append(candidates, candidateURL)
	}

	addCandidate(downloadRef)

	if rawURL, err := c.buildRawFileDownloadURL(storagePath); err == nil {
		addCandidate(rawURL)
	}

	addCandidate("/" + strings.TrimPrefix(strings.TrimSpace(storagePath), "/"))

	return candidates
}

func (c *PrusaLinkClient) buildRawFileDownloadURL(storagePath string) (string, error) {
	storagePath = strings.TrimPrefix(strings.TrimSpace(storagePath), "/")
	if storagePath == "" {
		return "", fmt.Errorf("storage path is empty")
	}

	parts := strings.Split(storagePath, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid storage path: %s", storagePath)
	}

	storage := url.PathEscape(parts[0])
	pathParts := make([]string, 0, len(parts)-1)
	for _, part := range parts[1:] {
		if part == "" {
			continue
		}
		pathParts = append(pathParts, url.PathEscape(part))
	}
	if len(pathParts) == 0 {
		return "", fmt.Errorf("invalid storage path: %s", storagePath)
	}

	return fmt.Sprintf("%s/api/files/%s/%s/raw", c.baseURL, storage, strings.Join(pathParts, "/")), nil
}

func (c *PrusaLinkClient) ParseGcodeFilamentUsageFromReader(reader io.Reader) (map[int]float64, error) {
	const chunkSize = 4096
	const carryBytes = 512

	rolling := make([]byte, 0, carryBytes+chunkSize)
	chunk := make([]byte, chunkSize)

	for {
		readCount, err := reader.Read(chunk)
		if readCount > 0 {
			rolling = append(rolling, chunk[:readCount]...)

			if usage := parseFilamentUsageFromContent(string(rolling)); len(usage) > 0 {
				return usage, nil
			}

			if len(rolling) > carryBytes {
				rolling = append([]byte(nil), rolling[len(rolling)-carryBytes:]...)
			}
		}

		if err == io.EOF {
			return nil, nil
		}
		if err != nil {
			if usage := parseFilamentUsageFromContent(string(rolling)); len(usage) > 0 {
				return usage, nil
			}
			return nil, fmt.Errorf("failed to read G-code stream: %w", err)
		}
	}
}

func (c *PrusaLinkClient) tryDownloadFilamentUsage(fileClient *http.Client, candidateURL string, useRange bool) (map[int]float64, error) {
	req, err := http.NewRequest("GET", candidateURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create G-code request: %w", err)
	}
	if useRange {
		req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", prusaLinkDownloadSniffBytes-1))
	}

	c.addAPIKey(req)

	resp, err := fileClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get G-code file from PrusaLink: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("PrusaLink API error: %d - %s", resp.StatusCode, string(body))
	}

	return c.ParseGcodeFilamentUsageFromReader(resp.Body)
}

func parseFilamentUsageFromContent(content string) map[int]float64 {
	filamentUsage := make(map[int]float64)

	// Parse both .gcode and .bgcode formats.
	// Examples:
	//   "filament used [g]=29.19"
	//   "; filament used [g] = 1.23, 4.56"
	gcodeRegex := regexp.MustCompile(`;?\s*filament used \[g\]\s*=\s*([0-9.,\s]+)`)
	gcodeMatch := gcodeRegex.FindStringSubmatch(content)
	if len(gcodeMatch) < 2 {
		return nil
	}

	weightsStr := gcodeMatch[1]
	weights := strings.Split(weightsStr, ",")
	for i, weightStr := range weights {
		weightStr = strings.TrimSpace(weightStr)
		if weight, err := strconv.ParseFloat(weightStr, 64); err == nil && weight > 0 {
			filamentUsage[i] = weight
		}
	}
	if len(filamentUsage) == 0 {
		return nil
	}

	return filamentUsage
}
