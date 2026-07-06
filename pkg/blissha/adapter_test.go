package blissha

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveHCI(t *testing.T) {
	got, err := resolveHCI("")
	require.NoError(t, err)
	require.Equal(t, "hci0", got)

	got, err = resolveHCI("hci1")
	require.NoError(t, err)
	require.Equal(t, "hci1", got)

	got, err = resolveHCI("  hci2  ")
	require.NoError(t, err)
	require.Equal(t, "hci2", got, "should trim whitespace")

	// A MAC that matches no adapter must error (rather than silently defaulting).
	_, err = resolveHCI("00:00:00:00:00:99")
	require.Error(t, err, "unknown adapter MAC should error")
}
