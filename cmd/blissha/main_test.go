package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/viggfred/blissble/pkg/blissha"
)

func TestDryRunReport(t *testing.T) {
	oslo, err := time.LoadLocation("Europe/Oslo")
	require.NoError(t, err)

	cfg := blissha.Config{
		MQTT:     blissha.MQTTConfig{Broker: "tcp://b:1883"},
		Location: &blissha.Location{Lat: 59.91, Lon: 10.75, Zone: oslo},
		Blinds: []blissha.BlindConfig{
			{Name: "West", MAC: "AA:BB:CC:DD:EE:03", Automation: blissha.Automation{Mode: blissha.ModeSunGlare, Window: blissha.Window{AzimuthDeg: 270, Sensitivity: 0.6}}},
			{Name: "Plain", MAC: "AA:BB:CC:DD:EE:04"}, // no automation → skipped
		},
	}
	require.NoError(t, cfg.Normalize())

	var buf bytes.Buffer
	dryRunReport(&buf, cfg)
	out := buf.String()

	require.Contains(t, out, "West")
	require.Contains(t, out, "sun_glare")
	require.Contains(t, out, "reason=")
	require.NotContains(t, out, "EE:04", "disabled blinds are omitted")
}
