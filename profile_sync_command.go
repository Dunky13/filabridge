package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"filabridge/profilesync"
)

func runProfileSync(parent context.Context, args []string, stdout io.Writer, httpClient *http.Client, slicer profilesync.Slicer) error {
	flags := flag.NewFlagSet("filabridge profile-sync", flag.ContinueOnError)
	flags.SetOutput(stdout)
	endpoint := flags.String("url", "", "FilaBridge /api/prusaslicer/profiles.zip URL")
	dataDir := flags.String("data-dir", "", "PrusaSlicer 3 data directory")
	executable := flags.String("prusa-slicer", "prusa-slicer", "PrusaSlicer executable")
	username := flags.String("username", os.Getenv("FILABRIDGE_SYNC_USERNAME"), "FilaBridge management username")
	allowInsecureHTTP := flags.Bool("allow-insecure-http", false, "allow plain HTTP on a trusted network")
	timeout := flags.Duration("timeout", 45*time.Second, "overall sync timeout")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("profile-sync accepts flags only")
	}
	if strings.TrimSpace(*endpoint) == "" {
		return fmt.Errorf("profile-sync requires --url")
	}
	if strings.TrimSpace(*dataDir) == "" {
		return fmt.Errorf("profile-sync requires explicit --data-dir")
	}
	if *timeout <= 0 {
		return fmt.Errorf("profile-sync --timeout must be positive")
	}
	if slicer == nil {
		slicer = profilesync.ExecSlicer{Executable: *executable}
	}

	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	result, err := profilesync.Sync(ctx, profilesync.SyncOptions{
		URL:               *endpoint,
		Username:          *username,
		Password:          os.Getenv("FILABRIDGE_SYNC_PASSWORD"),
		DataDir:           *dataDir,
		AllowInsecureHTTP: *allowInsecureHTTP,
		HTTPClient:        httpClient,
		Slicer:            slicer,
	})
	if err != nil {
		return err
	}
	state := "already current"
	if result.Changed {
		state = "updated"
	}
	_, err = fmt.Fprintf(stdout, "PrusaSlicer profiles %s: version %s\n%s\n", state, result.Version, result.Target)
	return err
}
