package main

import "sync"

// printerObservationSource is the single in-process source of current printer
// state. Monitoring writes observations by stable printer ID; web/status readers
// only consume snapshots, so a broadcast never starts a second printer poll.
type printerObservationSource struct {
	mutex       sync.RWMutex
	byPrinterID map[string]PrinterData
}

func newPrinterObservationSource() *printerObservationSource {
	return &printerObservationSource{byPrinterID: make(map[string]PrinterData)}
}

func (source *printerObservationSource) record(printerID string, data PrinterData) {
	source.mutex.Lock()
	source.byPrinterID[printerID] = data
	source.mutex.Unlock()
}

func (source *printerObservationSource) snapshot(configs map[string]PrinterConfig) map[string]PrinterData {
	source.mutex.RLock()
	defer source.mutex.RUnlock()

	result := make(map[string]PrinterData, len(configs))
	for printerID, config := range configs {
		if printerID == "no_printers" {
			continue
		}
		data, exists := source.byPrinterID[printerID]
		if !exists {
			data = PrinterData{State: StateOffline}
		}
		// Config owns display identity; observations are keyed by stable ID.
		data.Name = config.Name
		result[printerID] = data
	}
	return result
}

type polledPrinterObservation struct {
	data           PrinterData
	status         *PrusaLinkStatus
	job            *PrusaLinkJob
	sourcePath     string
	jobDisplayName string
	filamentUsage  map[int]float64
}
