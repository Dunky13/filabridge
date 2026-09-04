package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"filabridge/profilesync"

	"github.com/gin-gonic/gin"
)

func requiredProfileExportAuthMiddleware() gin.HandlerFunc {
	username := os.Getenv("FILABRIDGE_WEB_USERNAME")
	password := os.Getenv("FILABRIDGE_WEB_PASSWORD")
	if username == "" || password == "" {
		return func(c *gin.Context) {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "management authentication is required for profile export"})
		}
	}
	return gin.BasicAuth(gin.Accounts{username: password})
}

func (ws *WebServer) prusaSlicerProfilesHandler(c *gin.Context) {
	filaments, err := ws.bridge.spoolmanSnapshot().GetAllFilaments()
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("get filament definitions from Spoolman: %v", err)})
		return
	}

	exportable := make([]profilesync.Filament, 0, len(filaments))
	for _, filament := range filaments {
		vendor := ""
		if filament.Vendor != nil {
			vendor = filament.Vendor.Name
		}
		exportable = append(exportable, profilesync.Filament{
			ID:              filament.ID,
			Name:            filament.Name,
			Vendor:          vendor,
			Material:        filament.Material,
			Density:         filament.Density,
			Diameter:        filament.Diameter,
			SpoolWeight:     filament.SpoolWeight,
			CostPerKilogram: filamentCostPerKilogram(filament),
			ColorHex:        filament.ColorHex,
			Extra:           profileStringExtras(filament.Extra),
		})
	}

	archive, manifest, err := profilesync.Export(exportable)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	filename := fmt.Sprintf("filabridge-prusaslicer-profiles-%s.zip", manifest.Version)
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	c.Header("ETag", fmt.Sprintf(`"%s"`, manifest.ProfileSHA256))
	c.Header("X-FilaBridge-Profile-Version", manifest.Version)
	c.Data(http.StatusOK, "application/zip", archive)
}

func filamentCostPerKilogram(filament SpoolmanFilament) float64 {
	if filament.Price <= 0 || filament.Weight <= 0 {
		return 0
	}
	// Spoolman price is the price of the full filament definition and weight is
	// its net grams. PrusaSlicer filament_cost is cost per kilogram.
	return filament.Price * 1000 / filament.Weight
}

func profileStringExtras(extra map[string]interface{}) map[string]string {
	values := make(map[string]string)
	for key, value := range extra {
		if !strings.HasPrefix(key, "prusaslicer_") {
			continue
		}
		if typed, ok := value.(string); ok {
			values[key] = typed
		}
	}
	return values
}
