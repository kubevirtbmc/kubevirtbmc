package redfish

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

// TestComputerSystemResetPostUnsupportedResetType covers ResetType values
// that are valid in the Redfish ResetType enum but not in the
// ResetType@Redfish.AllowableValues this service advertises. They are a
// client error (400), not a service failure (500), and must not reach the
// ResourceManager.
func TestComputerSystemResetPostUnsupportedResetType(t *testing.T) {
	unsupported := []server.ResourceResetType{
		server.RESOURCERESETTYPE_FORCE_ON,
		server.RESOURCERESETTYPE_POWER_CYCLE,
		server.RESOURCERESETTYPE_PUSH_POWER_BUTTON,
		server.RESOURCERESETTYPE_NMI,
		server.RESOURCERESETTYPE_SUSPEND,
		server.RESOURCERESETTYPE_PAUSE,
		server.RESOURCERESETTYPE_RESUME,
	}

	for _, resetType := range unsupported {
		t.Run(string(resetType), func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRM := resourcemanager.NewMockResourceManager(ctrl) // no calls expected
			svc := NewAPIService(testUsername, testPassword, mockRM)

			resp, err := svc.RedfishV1SystemsComputerSystemIdActionsComputerSystemResetPost(
				context.Background(), "1", server.ComputerSystemV1220ResetRequestBody{ResetType: resetType})
			require.NoError(t, err)
			assert.Equal(t, http.StatusBadRequest, resp.Code)

			body, ok := resp.Body.(server.RedfishError)
			require.True(t, ok, "body should be a RedfishError, got %T", resp.Body)
			require.Len(t, body.Error.MessageExtendedInfo, 1)
			assert.Equal(t, "Base.1.10.ActionParameterValueNotInList", body.Error.MessageExtendedInfo[0].MessageId)
			assert.Contains(t, body.Error.MessageExtendedInfo[0].Message, string(resetType))
		})
	}
}

// TestComputerSystemResetPostSupportedResetTypeError checks that a real
// ResourceManager failure on a supported ResetType still maps to 500.
func TestComputerSystemResetPostSupportedResetTypeError(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	mockRM.EXPECT().PowerOn(gomock.Any()).Return(assert.AnError)
	svc := NewAPIService(testUsername, testPassword, mockRM)

	resp, err := svc.RedfishV1SystemsComputerSystemIdActionsComputerSystemResetPost(
		context.Background(), "1", server.ComputerSystemV1220ResetRequestBody{ResetType: server.RESOURCERESETTYPE_ON})
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, resp.Code)
}
