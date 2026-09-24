package redfish

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
	"kubevirt.io/kubevirtbmc/pkg/session"
)

const (
	testUsername = "admin"
	testPassword = "admin123"
)

func expectSetBootDevice(
	t *testing.T,
	mockRM *resourcemanager.MockResourceManager,
	device resourcemanager.BootDevice,
	mode resourcemanager.BootMode,
	returnErr error,
) {
	t.Helper()
	mockRM.EXPECT().
		SetBootDevice(gomock.Any(), device, gomock.AssignableToTypeOf(&resourcemanager.BootOptions{})).
		DoAndReturn(func(_ context.Context, _ resourcemanager.BootDevice, opts *resourcemanager.BootOptions) error {
			assert.Equal(t, mode, opts.Mode)
			assert.Nil(t, opts.EFIBoot)
			return returnErr
		})
}

func expectSetBootDeviceWithEFI(
	t *testing.T,
	mockRM *resourcemanager.MockResourceManager,
	device resourcemanager.BootDevice,
	mode resourcemanager.BootMode,
	efiBoot bool,
	returnErr error,
) {
	t.Helper()
	mockRM.EXPECT().
		SetBootDevice(gomock.Any(), device, gomock.AssignableToTypeOf(&resourcemanager.BootOptions{})).
		DoAndReturn(func(_ context.Context, _ resourcemanager.BootDevice, opts *resourcemanager.BootOptions) error {
			assert.Equal(t, mode, opts.Mode)
			if assert.NotNil(t, opts.EFIBoot) {
				assert.Equal(t, efiBoot, *opts.EFIBoot)
			}
			return returnErr
		})
}

func expectSetFirmwareMode(
	mockRM *resourcemanager.MockResourceManager,
	mode resourcemanager.FirmwareMode,
	returnErr error,
) {
	mockRM.EXPECT().SetFirmwareMode(gomock.Any(), mode).Return(returnErr)
}

func TestAuthenticate(t *testing.T) {
	ctl := gomock.NewController(t)
	defer ctl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctl)
	h := NewHandler(testUsername, testPassword, mockRM)

	testCases := []struct {
		username    string
		password    string
		expectError bool
	}{
		{username: "", password: "", expectError: true},
		{username: "invalid", password: "credentials", expectError: true},
		{username: "admin", password: "admin123", expectError: false},
	}

	for _, tc := range testCases {
		_, _, err := h.Authenticate(&tc.username, &tc.password)
		if tc.expectError {
			assert.Error(t, err)
		} else {
			assert.NoError(t, err)
		}
	}
}

func TestGetSession(t *testing.T) {
	ctl := gomock.NewController(t)
	defer ctl.Finish()

	h := NewHandler(testUsername, testPassword, nil)

	testCases := []struct {
		name          string
		setupFunc     func()
		sessionID     string
		username      string
		expectedError bool
	}{
		{
			name: "valid session",
			setupFunc: func() {
				tokenInfo := session.NewTokenInfo("test-session-id", "admin")
				session.AddToken(tokenInfo)
			},
			sessionID:     "test-session-id",
			username:      testUsername,
			expectedError: false,
		},
		{
			name:          "invalid session",
			setupFunc:     func() {},
			sessionID:     "invalid-session-id",
			username:      "",
			expectedError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setupFunc != nil {
				tc.setupFunc()
			}

			id, username, err := h.GetSession(tc.sessionID)
			if tc.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.sessionID, id)
				assert.Equal(t, tc.username, username)
			}
		})
	}
}

func TestGetSessionCollection(t *testing.T) {
	ctl := gomock.NewController(t)
	defer ctl.Finish()

	h := NewHandler(testUsername, testPassword, nil)

	tokenInfo := session.NewTokenInfo("test-session-id-collection", testUsername)
	token := session.AddToken(tokenInfo)
	defer session.RemoveToken(token)

	collection := h.GetSessionCollection()
	assert.Equal(t, "/redfish/v1/SessionService/Sessions", collection.OdataId)
	assert.Equal(t, int64(len(collection.Members)), collection.MembersodataCount)

	found := false
	for _, member := range collection.Members {
		if member.OdataId == "/redfish/v1/SessionService/Sessions/test-session-id-collection" {
			found = true
		}
	}
	assert.True(t, found, "session collection should contain the added session")
}

func TestDeleteSession(t *testing.T) {
	ctl := gomock.NewController(t)
	defer ctl.Finish()

	h := NewHandler(testUsername, testPassword, nil)

	testCases := []struct {
		name      string
		setupFunc func()
		sessionID string
	}{
		{
			name: "valid session",
			setupFunc: func() {
				tokenInfo := session.NewTokenInfo("test-session-id", testUsername)
				session.AddToken(tokenInfo)
			},
			sessionID: "test-session-id",
		},
		{
			name:      "non-existent session",
			setupFunc: func() {},
			sessionID: "invalid-session-id",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setupFunc != nil {
				tc.setupFunc()
			}

			h.DeleteSession(tc.sessionID)
			_, exists := session.GetTokenFromSessionID(tc.sessionID)
			assert.False(t, exists)
		})
	}
}

func TestPatchComputerSystem(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	handler := NewHandler(testUsername, testPassword, mockRM)

	testCases := []struct {
		name        string
		boot        server.ComputerSystemV1220Boot
		expectError bool
		mockSetup   func()
	}{
		{
			name: "disabled boot source override",
			boot: server.ComputerSystemV1220Boot{
				BootSourceOverrideEnabled: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_DISABLED,
			},
			mockSetup: func() {
				mockRM.EXPECT().ClearBootOverrides(gomock.Any()).Return(nil)
			},
			expectError: false,
		},
		{
			name: "valid boot source override target to PXE (once)",
			boot: server.ComputerSystemV1220Boot{
				BootSourceOverrideEnabled: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_ONCE,
				BootSourceOverrideTarget:  server.COMPUTERSYSTEMBOOTSOURCE_PXE,
			},
			mockSetup: func() {
				expectSetBootDevice(t, mockRM, resourcemanager.BootDevicePxe, resourcemanager.BootModeOneshot, nil)
			},
			expectError: false,
		},
		{
			name: "valid boot source override target to PXE (continuous)",
			boot: server.ComputerSystemV1220Boot{
				BootSourceOverrideEnabled: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_CONTINUOUS,
				BootSourceOverrideTarget:  server.COMPUTERSYSTEMBOOTSOURCE_PXE,
			},
			mockSetup: func() {
				expectSetBootDevice(t, mockRM, resourcemanager.BootDevicePxe, resourcemanager.BootModePersistent, nil)
			},
			expectError: false,
		},
		{
			name: "valid boot source override target to HDD (once)",
			boot: server.ComputerSystemV1220Boot{
				BootSourceOverrideEnabled: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_ONCE,
				BootSourceOverrideTarget:  server.COMPUTERSYSTEMBOOTSOURCE_HDD,
			},
			mockSetup: func() {
				expectSetBootDevice(t, mockRM, resourcemanager.BootDeviceHdd, resourcemanager.BootModeOneshot, nil)
			},
			expectError: false,
		},
		{
			name: "valid boot source override target to HDD (continuous)",
			boot: server.ComputerSystemV1220Boot{
				BootSourceOverrideEnabled: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_CONTINUOUS,
				BootSourceOverrideTarget:  server.COMPUTERSYSTEMBOOTSOURCE_HDD,
			},
			mockSetup: func() {
				expectSetBootDevice(t, mockRM, resourcemanager.BootDeviceHdd, resourcemanager.BootModePersistent, nil)
			},
			expectError: false,
		},
		{
			name: "invalid boot source override target (once)",
			boot: server.ComputerSystemV1220Boot{
				BootSourceOverrideEnabled: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_ONCE,
				BootSourceOverrideTarget:  "INVALID_TARGET",
			},
			mockSetup:   func() {},
			expectError: false,
		},
		{
			name: "invalid boot source override target (continuous)",
			boot: server.ComputerSystemV1220Boot{
				BootSourceOverrideEnabled: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_CONTINUOUS,
				BootSourceOverrideTarget:  "INVALID_TARGET",
			},
			mockSetup:   func() {},
			expectError: false,
		},
		{
			name: "failed to set PXE boot device",
			boot: server.ComputerSystemV1220Boot{
				BootSourceOverrideEnabled: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_ONCE,
				BootSourceOverrideTarget:  server.COMPUTERSYSTEMBOOTSOURCE_PXE,
			},
			mockSetup: func() {
				expectSetBootDevice(t, mockRM, resourcemanager.BootDevicePxe, resourcemanager.BootModeOneshot, assert.AnError)
			},
			expectError: true,
		},
		{
			name: "failed to set HDD boot device",
			boot: server.ComputerSystemV1220Boot{
				BootSourceOverrideEnabled: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_ONCE,
				BootSourceOverrideTarget:  server.COMPUTERSYSTEMBOOTSOURCE_HDD,
			},
			mockSetup: func() {
				expectSetBootDevice(t, mockRM, resourcemanager.BootDeviceHdd, resourcemanager.BootModeOneshot, assert.AnError)
			},
			expectError: true,
		},
		{
			name: "valid boot source override target to Cd (once)",
			boot: server.ComputerSystemV1220Boot{
				BootSourceOverrideEnabled: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_ONCE,
				BootSourceOverrideTarget:  server.COMPUTERSYSTEMBOOTSOURCE_CD,
			},
			mockSetup: func() {
				expectSetBootDevice(t, mockRM, resourcemanager.BootDeviceCd, resourcemanager.BootModeOneshot, nil)
			},
			expectError: false,
		},
		{
			name: "valid boot source override target to Cd (continuous)",
			boot: server.ComputerSystemV1220Boot{
				BootSourceOverrideEnabled: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_CONTINUOUS,
				BootSourceOverrideTarget:  server.COMPUTERSYSTEMBOOTSOURCE_CD,
			},
			mockSetup: func() {
				expectSetBootDevice(t, mockRM, resourcemanager.BootDeviceCd, resourcemanager.BootModePersistent, nil)
			},
			expectError: false,
		},
		{
			name: "failed to set Cd boot device",
			boot: server.ComputerSystemV1220Boot{
				BootSourceOverrideEnabled: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_ONCE,
				BootSourceOverrideTarget:  server.COMPUTERSYSTEMBOOTSOURCE_CD,
			},
			mockSetup: func() {
				expectSetBootDevice(t, mockRM, resourcemanager.BootDeviceCd, resourcemanager.BootModeOneshot, assert.AnError)
			},
			expectError: true,
		},
		{
			name: "valid boot source override target to PXE once with UEFI mode",
			boot: server.ComputerSystemV1220Boot{
				BootSourceOverrideEnabled: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_ONCE,
				BootSourceOverrideTarget:  server.COMPUTERSYSTEMBOOTSOURCE_PXE,
				BootSourceOverrideMode:    server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEMODE_UEFI,
			},
			mockSetup: func() {
				expectSetBootDeviceWithEFI(t, mockRM, resourcemanager.BootDevicePxe, resourcemanager.BootModeOneshot, true, nil)
			},
			expectError: false,
		},
		{
			name: "valid boot source override target to HDD continuous with Legacy mode",
			boot: server.ComputerSystemV1220Boot{
				BootSourceOverrideEnabled: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_CONTINUOUS,
				BootSourceOverrideTarget:  server.COMPUTERSYSTEMBOOTSOURCE_HDD,
				BootSourceOverrideMode:    server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEMODE_LEGACY,
			},
			mockSetup: func() {
				expectSetBootDeviceWithEFI(t, mockRM, resourcemanager.BootDeviceHdd, resourcemanager.BootModePersistent, false, nil)
			},
			expectError: false,
		},
		{
			name: "valid UEFI mode without boot override",
			boot: server.ComputerSystemV1220Boot{
				BootSourceOverrideMode: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEMODE_UEFI,
			},
			mockSetup: func() {
				mockRM.EXPECT().GetBootOverride(gomock.Any()).Return(nil, nil)
				expectSetFirmwareMode(mockRM, resourcemanager.FirmwareModeUEFI, nil)
			},
			expectError: false,
		},
		{
			name: "valid Legacy mode without boot override",
			boot: server.ComputerSystemV1220Boot{
				BootSourceOverrideMode: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEMODE_LEGACY,
			},
			mockSetup: func() {
				mockRM.EXPECT().GetBootOverride(gomock.Any()).Return(nil, nil)
				expectSetFirmwareMode(mockRM, resourcemanager.FirmwareModeLegacy, nil)
			},
			expectError: false,
		},
		{
			name: "failed to set UEFI mode without boot override",
			boot: server.ComputerSystemV1220Boot{
				BootSourceOverrideMode: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEMODE_UEFI,
			},
			mockSetup: func() {
				mockRM.EXPECT().GetBootOverride(gomock.Any()).Return(nil, nil)
				expectSetFirmwareMode(mockRM, resourcemanager.FirmwareModeUEFI, assert.AnError)
			},
			expectError: true,
		},
		{
			name: "target-only PATCH applies under active continuous override",
			boot: server.ComputerSystemV1220Boot{
				BootSourceOverrideTarget: server.COMPUTERSYSTEMBOOTSOURCE_CD,
			},
			mockSetup: func() {
				mockRM.EXPECT().GetBootOverride(gomock.Any()).Return(&bmcv1.BootOverrideStatus{
					Mode: bmcv1.BootOverrideModePersistent,
				}, nil)
				expectSetBootDevice(t, mockRM, resourcemanager.BootDeviceCd, resourcemanager.BootModePersistent, nil)
			},
			expectError: false,
		},
		{
			name: "target-only PATCH applies under active oneshot override",
			boot: server.ComputerSystemV1220Boot{
				BootSourceOverrideTarget: server.COMPUTERSYSTEMBOOTSOURCE_PXE,
			},
			mockSetup: func() {
				mockRM.EXPECT().GetBootOverride(gomock.Any()).Return(&bmcv1.BootOverrideStatus{
					Mode: bmcv1.BootOverrideModeOneshot,
				}, nil)
				expectSetBootDevice(t, mockRM, resourcemanager.BootDevicePxe, resourcemanager.BootModeOneshot, nil)
			},
			expectError: false,
		},
		{
			name: "target-only PATCH without active override is a no-op",
			boot: server.ComputerSystemV1220Boot{
				BootSourceOverrideTarget: server.COMPUTERSYSTEMBOOTSOURCE_CD,
			},
			mockSetup: func() {
				mockRM.EXPECT().GetBootOverride(gomock.Any()).Return(nil, nil)
			},
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.mockSetup()

			err := handler.PatchComputerSystem(context.Background(), &server.ComputerSystemV1220ComputerSystem{
				Boot: tc.boot,
			})

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestGetComputerSystemBootOverrideFallback covers the degenerate state where
// GetBootFlags fails (no device carries a boot order): the Enabled bit must
// come from the state store, not the in-memory ComputerSystem model — which
// may hold an already-consumed override, or Disabled while one is still
// recorded after an agent restart.
func TestGetComputerSystemBootOverrideFallback(t *testing.T) {
	cases := []struct {
		name        string
		flags       *resourcemanager.BootFlagsState
		flagsErr    error
		override    *bmcv1.BootOverrideStatus // consulted only when flagsErr is set
		wantEnabled server.ComputerSystemV1220BootSourceOverrideEnabled
	}{
		{
			name: "flags OK with active override renders Once",
			flags: &resourcemanager.BootFlagsState{
				BootDevice:     resourcemanager.BootDevicePxe,
				Mode:           resourcemanager.BootModeOneshot,
				OverrideActive: true,
			},
			wantEnabled: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_ONCE,
		},
		{
			name:        "flags error without recorded override renders Disabled",
			flagsErr:    assert.AnError,
			wantEnabled: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_DISABLED,
		},
		{
			name:        "flags error with recorded oneshot override renders Once",
			flagsErr:    assert.AnError,
			override:    &bmcv1.BootOverrideStatus{Mode: bmcv1.BootOverrideModeOneshot},
			wantEnabled: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_ONCE,
		},
		{
			name:        "flags error with recorded persistent override renders Continuous",
			flagsErr:    assert.AnError,
			override:    &bmcv1.BootOverrideStatus{Mode: bmcv1.BootOverrideModePersistent},
			wantEnabled: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_CONTINUOUS,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRM := resourcemanager.NewMockResourceManager(ctrl)
			mockRM.EXPECT().GetComputerSystem(gomock.Any()).Return(
				resourcemanager.NewComputerSystem("1", "test", "", "", server.RESOURCEPOWERSTATE_ON), nil)
			mockRM.EXPECT().GetBootFlags(gomock.Any()).Return(tc.flags, tc.flagsErr)
			if tc.flagsErr != nil {
				mockRM.EXPECT().GetBootOverride(gomock.Any()).Return(tc.override, nil)
			}

			handler := NewHandler(testUsername, testPassword, mockRM)
			cs, err := handler.GetComputerSystem(context.Background())
			assert.NoError(t, err)
			assert.Equal(t, tc.wantEnabled, cs.Boot.BootSourceOverrideEnabled)
		})
	}
}

func TestComputerSystemReset(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	handler := NewHandler(testUsername, testPassword, mockRM)

	testCases := []struct {
		name          string
		resetType     server.ResourceResetType
		mockSetup     func()
		expectedError bool
	}{
		{
			name:      "power on reset",
			resetType: server.RESOURCERESETTYPE_ON,
			mockSetup: func() {
				mockRM.EXPECT().PowerOn(gomock.Any()).Return(nil)
			},
			expectedError: false,
		},
		{
			name:      "graceful shutdown reset",
			resetType: server.RESOURCERESETTYPE_GRACEFUL_SHUTDOWN,
			mockSetup: func() {
				mockRM.EXPECT().PowerOff(gomock.Any()).Return(nil)
			},
			expectedError: false,
		},
		{
			name:      "force off reset",
			resetType: server.RESOURCERESETTYPE_FORCE_OFF,
			mockSetup: func() {
				mockRM.EXPECT().ForcePowerOff(gomock.Any()).Return(nil)
			},
			expectedError: false,
		},
		{
			name:      "graceful restart reset",
			resetType: server.RESOURCERESETTYPE_GRACEFUL_RESTART,
			mockSetup: func() {
				mockRM.EXPECT().PowerCycle(gomock.Any()).Return(nil)
			},
			expectedError: false,
		},
		{
			name:      "force restart reset",
			resetType: server.RESOURCERESETTYPE_FORCE_RESTART,
			mockSetup: func() {
				mockRM.EXPECT().ForcePowerCycle(gomock.Any()).Return(nil)
			},
			expectedError: false,
		},
		{
			name:          "unsupported reset type",
			resetType:     server.ResourceResetType("Unsupported"),
			mockSetup:     func() {}, // No expectations for unsupported reset types
			expectedError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.mockSetup()
			err := handler.ComputerSystemReset(context.Background(), tc.resetType)
			if tc.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
