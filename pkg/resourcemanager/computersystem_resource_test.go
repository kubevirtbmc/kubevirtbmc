package resourcemanager

import (
	"testing"

	"github.com/stretchr/testify/require"
	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
)

func TestNewComputerSystemAdvertisesResetAllowableValues(t *testing.T) {
	computerSystem := NewComputerSystem("1", "test-namespace/test-vm", server.RESOURCEPOWERSTATE_OFF).ComputerSystem()

	require.ElementsMatch(t, []server.ResourceResetType{
		server.RESOURCERESETTYPE_ON,
		server.RESOURCERESETTYPE_FORCE_OFF,
		server.RESOURCERESETTYPE_GRACEFUL_SHUTDOWN,
		server.RESOURCERESETTYPE_GRACEFUL_RESTART,
		server.RESOURCERESETTYPE_FORCE_RESTART,
	}, computerSystem.Actions.ComputerSystemReset.ResetTypeRedfishAllowableValues)
}

func TestNewComputerSystemSerialNumberMatchesName(t *testing.T) {
	name := "test-namespace/test-vm"
	cs := NewComputerSystem("1", name, server.RESOURCEPOWERSTATE_OFF).ComputerSystem()
	require.Equal(t, name, cs.Name)
	require.NotNil(t, cs.SerialNumber)
	require.Equal(t, name, *cs.SerialNumber)
}
