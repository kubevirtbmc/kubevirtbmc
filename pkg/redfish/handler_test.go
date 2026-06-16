package redfish

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
	"kubevirt.io/kubevirtbmc/pkg/session"
)

const (
	testUsername = "admin"
	testPassword = "admin123"
)

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
			_, exists := session.GetToken(tc.sessionID)
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
			mockSetup:   func() {},
			expectError: false,
		},
		{
			name: "valid boot source override target to PXE (once)",
			boot: server.ComputerSystemV1220Boot{
				BootSourceOverrideEnabled: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_ONCE,
				BootSourceOverrideTarget:  server.COMPUTERSYSTEMBOOTSOURCE_PXE,
			},
			mockSetup: func() {
				mockRM.EXPECT().SetBootDevice(resourcemanager.BootDevicePxe).Return(nil)
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
				mockRM.EXPECT().SetBootDevice(resourcemanager.BootDevicePxe).Return(nil)
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
				mockRM.EXPECT().SetBootDevice(resourcemanager.BootDeviceHdd).Return(nil)
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
				mockRM.EXPECT().SetBootDevice(resourcemanager.BootDeviceHdd).Return(nil)
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
				mockRM.EXPECT().SetBootDevice(resourcemanager.BootDevicePxe).Return(assert.AnError)
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
				mockRM.EXPECT().SetBootDevice(resourcemanager.BootDeviceHdd).Return(assert.AnError)
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
				mockRM.EXPECT().SetBootDevice(resourcemanager.BootDeviceCd).Return(nil)
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
				mockRM.EXPECT().SetBootDevice(resourcemanager.BootDeviceCd).Return(nil)
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
				mockRM.EXPECT().SetBootDevice(resourcemanager.BootDeviceCd).Return(assert.AnError)
			},
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.mockSetup()

			err := handler.PatchComputerSystem(&server.ComputerSystemV1220ComputerSystem{
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
				mockRM.EXPECT().PowerOn().Return(nil)
			},
			expectedError: false,
		},
		{
			name:      "graceful shutdown reset",
			resetType: server.RESOURCERESETTYPE_GRACEFUL_SHUTDOWN,
			mockSetup: func() {
				mockRM.EXPECT().PowerOff().Return(nil)
			},
			expectedError: false,
		},
		{
			name:      "force off reset",
			resetType: server.RESOURCERESETTYPE_FORCE_OFF,
			mockSetup: func() {
				mockRM.EXPECT().ForcePowerOff().Return(nil)
			},
			expectedError: false,
		},
		{
			name:      "graceful restart reset",
			resetType: server.RESOURCERESETTYPE_GRACEFUL_RESTART,
			mockSetup: func() {
				mockRM.EXPECT().PowerCycle().Return(nil)
			},
			expectedError: false,
		},
		{
			name:      "force restart reset",
			resetType: server.RESOURCERESETTYPE_FORCE_RESTART,
			mockSetup: func() {
				mockRM.EXPECT().ForcePowerCycle().Return(nil)
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
			err := handler.ComputerSystemReset(tc.resetType)
			if tc.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
