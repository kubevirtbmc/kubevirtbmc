package resourcemanager

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewManagerReportsFirmwareVersion(t *testing.T) {
	m := NewManager("1", "BMC", "3b7bbc8b559d8a712e502afd9d1cb9251aacb2f3").Manager()
	require.NotNil(t, m.FirmwareVersion)
	require.Equal(t, "3b7bbc8b559d8a712e502afd9d1cb9251aacb2f3", *m.FirmwareVersion)
}
