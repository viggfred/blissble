// Command blissha bridges Hunter Douglas Bliss Smart Blinds to Home Assistant
// over MQTT. It exposes each configured blind as an MQTT Cover (via HA MQTT
// discovery), so standard Home Assistant controls work with no custom UI.
//
//	blissha -config /config/config.yaml
//
// It manages 0..many blinds across one or more Bluetooth adapters over a single
// MQTT connection, and is designed to run as a container (see the Containerfile).
// The reusable bridge lives in github.com/viggfred/blissble/pkg/blissha; this
// command just wraps it with YAML/CLI configuration.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/viggfred/blissble/pkg/blissha"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to the YAML config file")
	debug := flag.Bool("debug", false, "enable debug logging")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	bridge, err := blissha.New(cfg, logger)
	if err != nil {
		logger.Error("invalid config", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := bridge.Run(ctx); err != nil {
		logger.Error("bridge stopped with error", "error", err)
		os.Exit(1)
	}
}
