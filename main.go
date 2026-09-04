package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(parent context.Context, args []string) error {
	if len(args) > 0 && args[0] == "profile-sync" {
		return runProfileSync(parent, args[1:], os.Stdout, nil, nil)
	}

	// Command line flags
	var (
		flags      = flag.NewFlagSet("filabridge", flag.ContinueOnError)
		webOnly    = flags.Bool("web-only", false, "Run only the web interface")
		bridgeOnly = flags.Bool("bridge-only", false, "Run only the bridge service")
		port       = flags.String("port", DefaultWebPort, "Web interface port")
		host       = flags.String("host", "127.0.0.1", "Web interface host")
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *webOnly && *bridgeOnly {
		return fmt.Errorf("--web-only and --bridge-only cannot be used together")
	}

	// Create bridge instance first (with default config)
	bridge, err := NewFilamentBridge(nil)
	if err != nil {
		return fmt.Errorf("create bridge: %w", err)
	}
	defer bridge.Close()

	// Load configuration from database
	config, err := LoadConfig(bridge)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	// Update bridge with loaded config
	if err := bridge.UpdateConfig(config); err != nil {
		return fmt.Errorf("update bridge config: %w", err)
	}
	// Override port from config if not specified
	if *port == DefaultWebPort && config.WebPort != DefaultWebPort {
		*port = config.WebPort
	}

	mode := RuntimeModeCombined
	if *webOnly {
		mode = RuntimeModeWebOnly
	} else if *bridgeOnly {
		mode = RuntimeModeBridgeOnly
	}

	var webServer *WebServer
	if mode != RuntimeModeBridgeOnly {
		webServer = NewWebServerForHost(bridge, *host)
	}
	runtime, err := NewApplicationRuntime(bridge, webServer, RuntimeOptions{
		Mode: mode,
		Host: *host,
		Port: *port,
	})
	if err != nil {
		return err
	}

	fmt.Printf("Mode: %s\n", mode)
	fmt.Printf("Monitoring printers: %v\n", getPrinterNames(config))
	fmt.Printf("Spoolman URL: %s\n", config.SpoolmanURL)
	fmt.Printf("Poll interval: %v\n", config.PollInterval)
	if mode != RuntimeModeBridgeOnly {
		fmt.Printf("Web interface: http://%s\n", runtime.options.ListenAddress())
	}

	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return runtime.Run(ctx)
}

// getPrinterNames returns a slice of printer names from config
func getPrinterNames(config *Config) []string {
	names := make([]string, 0, len(config.Printers))
	for name := range config.Printers {
		names = append(names, name)
	}
	return names
}
