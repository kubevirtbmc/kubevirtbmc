package resourcemanager

import (
	"testing"

	"github.com/stretchr/testify/require"
	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
)

func TestBootDeviceToRedfishTarget(t *testing.T) {
	require.Equal(t, server.COMPUTERSYSTEMBOOTSOURCE_HDD, BootDeviceToRedfishTarget(BootDeviceHdd))
	require.Equal(t, server.COMPUTERSYSTEMBOOTSOURCE_PXE, BootDeviceToRedfishTarget(BootDevicePxe))
	require.Equal(t, server.COMPUTERSYSTEMBOOTSOURCE_CD, BootDeviceToRedfishTarget(BootDeviceCd))
	// "" (no device carries a bootOrder) must render as None, not the zero
	// value — the latter is omitempty-dropped from the JSON response.
	require.Equal(t, server.COMPUTERSYSTEMBOOTSOURCE_NONE, BootDeviceToRedfishTarget(""))
}
