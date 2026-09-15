package resourcemanager

import (
	"testing"

	"github.com/stretchr/testify/require"
	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
)

func TestNewComputerSystemReportsVMIdentity(t *testing.T) {
	name := "test-namespace/test-vm"
	uuid := "5d307ca9-b3ef-428c-8861-06e72d69f223"
	serial := "e4686d2c-6e8d-4335-b8fd-81bee22f4815"
	cs := NewComputerSystem("1", name, uuid, serial, server.RESOURCEPOWERSTATE_OFF).ComputerSystem()
	require.Equal(t, name, cs.Name)
	require.Equal(t, uuid, cs.UUID)
	require.NotNil(t, cs.SerialNumber)
	require.Equal(t, serial, *cs.SerialNumber)
}
