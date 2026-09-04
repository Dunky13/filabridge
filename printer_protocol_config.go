package main

import (
	"log"
)

func newConfiguredPrusaLinkClient(config PrinterConfig, timeout int, fileDownloadTimeout int) (*PrusaLinkClient, error) {
	return NewPrusaLinkClientWithOptions(PrusaLinkClientOptions{
		BaseURL:             config.IPAddress,
		APIKey:              config.APIKey,
		DigestUsername:      config.PrusaLinkUsername,
		DigestPassword:      config.PrusaLinkPassword,
		CustomCAPEM:         []byte(config.PrusaLinkCustomCAPEM),
		Timeout:             timeout,
		FileDownloadTimeout: fileDownloadTimeout,
	})
}

func (b *FilamentBridge) logPrusaLinkDiagnosticsOnce(printerID string, client *PrusaLinkClient) {
	b.mutex.Lock()
	if b.diagnosticsLogged[printerID] {
		b.mutex.Unlock()
		return
	}
	b.diagnosticsLogged[printerID] = true
	b.mutex.Unlock()

	diagnostics, err := client.GetDiagnostics()
	if err != nil {
		log.Printf("PrusaLink diagnostics unavailable for %s: %v", printerID, err)
		return
	}
	log.Printf("PrusaLink %s: API=%s server=%s firmware=%s printer=%s HTTPS=%t capabilities=%+v",
		printerID, diagnostics.API, diagnostics.Server, diagnostics.Firmware,
		diagnostics.Printer, diagnostics.HTTPS, diagnostics.Capabilities)
}
