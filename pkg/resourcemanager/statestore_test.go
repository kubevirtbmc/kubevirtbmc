package resourcemanager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
)

func TestFileStateStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")

	store, err := NewFileStateStore(path)
	require.NoError(t, err)

	// Fresh store: no override recorded.
	override, err := store.GetBootOverride(context.Background())
	require.NoError(t, err)
	require.Nil(t, override)

	// Save persists across store instances (agent restart).
	saved := &bmcv1.BootOverrideStatus{
		Mode:             bmcv1.BootOverrideModeOneshot,
		VMUID:            "vm-uid-1",
		VMIUID:           "uid-1",
		BootOrders:       map[string]uint{"disk:root": 1},
		OriginalFirmware: bmcv1.FirmwareTypeUEFI,
	}
	require.NoError(t, store.SaveBootOverride(context.Background(), saved))

	reloaded, err := NewFileStateStore(path)
	require.NoError(t, err)
	override, err = reloaded.GetBootOverride(context.Background())
	require.NoError(t, err)
	require.Equal(t, saved, override)

	// Mutating the returned copy must not affect stored state.
	override.BootOrders["disk:root"] = 99
	override, err = reloaded.GetBootOverride(context.Background())
	require.NoError(t, err)
	require.Equal(t, uint(1), override.BootOrders["disk:root"])

	// Clear persists too.
	require.NoError(t, reloaded.ClearBootOverride(context.Background()))
	reloaded, err = NewFileStateStore(path)
	require.NoError(t, err)
	override, err = reloaded.GetBootOverride(context.Background())
	require.NoError(t, err)
	require.Nil(t, override)

	// Clearing an absent override is not an error.
	require.NoError(t, reloaded.ClearBootOverride(context.Background()))

	// A corrupt state file fails loudly instead of silently losing a pending backup.
	require.NoError(t, os.WriteFile(path, []byte("{"), 0o600))
	_, err = NewFileStateStore(path)
	require.Error(t, err)
}

// The state file is an envelope: boot override lives under its own top-level
// key so future persisted state (e.g. Redfish task records) gets a sibling
// key instead of colliding with override fields.
func TestFileStateStoreEnvelopeShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	store, err := NewFileStateStore(path)
	require.NoError(t, err)
	require.NoError(t, store.SaveBootOverride(context.Background(),
		&bmcv1.BootOverrideStatus{Mode: bmcv1.BootOverrideModePersistent, VMUID: "vm-uid-1"}))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var file map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &file))
	require.Contains(t, file, "bootOverride")
	require.NotContains(t, file, "mode")
}

func TestFileStateStoreFailedSaveKeepsCommittedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	store, err := NewFileStateStore(path)
	require.NoError(t, err)

	committed := &bmcv1.BootOverrideStatus{
		Mode:       bmcv1.BootOverrideModeOneshot,
		VMIUID:     "uid-1",
		BootOrders: map[string]uint{"disk:root": 1},
	}
	require.NoError(t, store.SaveBootOverride(context.Background(), committed))

	// Make the next persist fail: renaming the tmp file onto a directory errors.
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Mkdir(path, 0o755))
	err = store.SaveBootOverride(context.Background(), &bmcv1.BootOverrideStatus{Mode: bmcv1.BootOverrideModePersistent, VMIUID: "uid-2"})
	require.Error(t, err)

	// The failed save must not displace the committed override in memory.
	override, err := store.GetBootOverride(context.Background())
	require.NoError(t, err)
	require.Equal(t, committed, override)
}
