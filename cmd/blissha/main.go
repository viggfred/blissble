// Command blissha bridges Hunter Douglas Bliss Smart Blinds to Home Assistant
// over MQTT. It exposes each configured blind as an MQTT Cover (via HA MQTT
// discovery), so standard Home Assistant controls work with no custom UI.
//
//	blissha -config /config/config.yaml
//
// It manages 0..many blinds across one or more Bluetooth adapters over a single
// MQTT connection, and is designed to run as a container (see the Containerfile).
// The reusable bridge lives in github.com/viggfred/blissble/pkg/blissha; this
// command just wraps it with the YAML schema from pkg/blissha/yamlcfg.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // embed the IANA tz database so location.timezone resolves anywhere

	"github.com/viggfred/blissble/pkg/blissha"
	"github.com/viggfred/blissble/pkg/blissha/yamlcfg"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to the YAML config file")
	debug := flag.Bool("debug", false, "enable debug logging")
	dryRun := flag.Bool("dry-run", false, "print each blind's automation decisions over 24h and exit (no MQTT/BLE)")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	cfg, err := yamlcfg.Load(*configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	if *dryRun {
		if err := cfg.Normalize(); err != nil {
			logger.Error("invalid config", "error", err)
			os.Exit(1)
		}
		dryRunReport(os.Stdout, cfg)
		return
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

// dryRunReport prints, for every blind with automation enabled, the target
// position and reason the controller would choose at each hour of today — so the
// sun/schedule behavior can be eyeballed without touching Bluetooth or MQTT. It
// assumes the room is occupied and a mid position (50%), and evaluates each hour
// independently (so battery move-gating doesn't mask intent).
func dryRunReport(w io.Writer, cfg blissha.Config) {
	zone := time.Local
	if cfg.Location != nil && cfg.Location.Zone != nil {
		zone = cfg.Location.Zone
	}
	now := time.Now().In(zone)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, zone)

	var loc blissha.Location
	if cfg.Location != nil {
		loc = *cfg.Location
	}

	fmt.Fprintf(w, "Dry run for %s (assuming room occupied, current position 50%%)\n", start.Format("2006-01-02 MST"))
	printed := false
	for _, b := range cfg.Blinds {
		if b.Automation.Mode == blissha.ModeOff {
			continue
		}
		printed = true
		fmt.Fprintf(w, "\n%s (%s) — mode=%s\n", b.Name, b.MAC, b.Automation.Mode)
		for h := range 24 {
			t := start.Add(time.Duration(h) * time.Hour)
			d := blissha.Decide(blissha.DecisionInput{
				Cfg:     b.Automation,
				Loc:     loc,
				Now:     t,
				Current: blissha.Position{HA: 50, Known: true},
				Signals: blissha.Signals{RoomOccupied: blissha.TriYes},
			})
			fmt.Fprintf(w, "  %s  target=%3d  reason=%s\n", t.Format("15:04"), d.TargetHA, d.Reason)
		}
	}
	if !printed {
		fmt.Fprintln(w, "\n(no blinds have automation enabled)")
	}
}
