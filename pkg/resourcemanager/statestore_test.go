package resourcemanager

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
)

func TestFileStateStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")

	store, err := NewFileStateStore(path, "fast-sc")
	require.NoError(t, err)

	// Fresh store: no override recorded.
	override, err := store.GetBootOverride()
	require.NoError(t, err)
	require.Nil(t, override)
	sc, err := store.GetStorageClassName()
	require.NoError(t, err)
	require.Equal(t, "fast-sc", sc)

	// Save persists across store instances (agent restart).
	saved := &bmcv1.BootOverrideStatus{
		Mode:             bmcv1.BootOverrideModeOneshot,
		VMIUID:           "uid-1",
		BootOrders:       map[string]uint{"disk:root": 1},
		OriginalFirmware: bmcv1.FirmwareTypeUEFI,
	}
	require.NoError(t, store.SaveBootOverride(saved))

	reloaded, err := NewFileStateStore(path, "fast-sc")
	require.NoError(t, err)
	override, err = reloaded.GetBootOverride()
	require.NoError(t, err)
	require.Equal(t, saved, override)

	// Mutating the returned copy must not affect stored state.
	override.BootOrders["disk:root"] = 99
	override, err = reloaded.GetBootOverride()
	require.NoError(t, err)
	require.Equal(t, uint(1), override.BootOrders["disk:root"])

	// Clear persists too.
	require.NoError(t, reloaded.ClearBootOverride())
	reloaded, err = NewFileStateStore(path, "fast-sc")
	require.NoError(t, err)
	override, err = reloaded.GetBootOverride()
	require.NoError(t, err)
	require.Nil(t, override)

	// Clearing an absent override is not an error.
	require.NoError(t, reloaded.ClearBootOverride())

	// A corrupt state file fails loudly instead of silently losing a pending backup.
	require.NoError(t, os.WriteFile(path, []byte("{"), 0o600))
	_, err = NewFileStateStore(path, "fast-sc")
	require.Error(t, err)
}
