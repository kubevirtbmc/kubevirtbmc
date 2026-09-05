package ipmi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/bougou/go-ipmi/pkg/hal"
	"github.com/bougou/go-ipmi/pkg/types"

	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

// TestVMChassisPowerStateDelegates verifies the HAL chassis adapter feeds
// go-ipmi's built-in Get Chassis Status handler with the VM power state.
func TestVMChassisPowerStateDelegates(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	mockRM.EXPECT().GetPowerStatus(gomock.Any()).Return(true, nil)

	c := vmChassis{rm: mockRM}
	on, err := c.PowerState(context.Background())
	assert.NoError(t, err)
	assert.True(t, on)
}

// TestVMChassisSetPower asserts the spec Table 28-3 power-up/power-down
// dispatch lands on the KubeVirt ResourceManager start/stop APIs. Power Down
// (0x00, immediate) routes through SetPower(false) → ForcePowerOff(); Power
// Up (0x01) routes through SetPower(true) → PowerOn().
func TestVMChassisSetPower(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	mockRM.EXPECT().PowerOn(gomock.Any()).Return(nil)
	mockRM.EXPECT().ForcePowerOff(gomock.Any()).Return(nil)

	c := vmChassis{rm: mockRM}
	assert.NoError(t, c.SetPower(context.Background(), true))
	assert.NoError(t, c.SetPower(context.Background(), false))
}

// TestVMChassisPowerCycle asserts Chassis Control Power Cycle (0x02) routes
// to rm.ForcePowerCycle() (GracePeriodSeconds=0). Per IPMI spec §28.3, Power
// Cycle cuts power entirely — the most disruptive reset — so it maps to the
// force variant.
func TestVMChassisPowerCycle(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	mockRM.EXPECT().ForcePowerCycle(gomock.Any()).Return(nil)

	c := vmChassis{rm: mockRM}
	assert.NoError(t, c.PowerCycle(context.Background()))
}

// TestVMChassisColdResetMapsToPowerCycle asserts Hard Reset (0x03) maps to a
// graceful KubeVirt restart (rm.PowerCycle). Per IPMI spec §28.3, Hard Reset
// asserts the system reset line without power cycling, making it less
// disruptive than a full Power Cycle (0x02).
func TestVMChassisColdResetMapsToPowerCycle(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	mockRM.EXPECT().PowerCycle(gomock.Any()).Return(nil)

	c := vmChassis{rm: mockRM}
	assert.NoError(t, c.ColdReset(context.Background()))
}

// TestVMChassisWarmResetMapsToPowerOff asserts ACPI Soft Shutdown (0x05),
// which go-ipmi dispatches to WarmReset, maps to a graceful KubeVirt stop.
func TestVMChassisWarmResetMapsToPowerOff(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	mockRM.EXPECT().PowerOff(gomock.Any()).Return(nil)

	c := vmChassis{rm: mockRM}
	assert.NoError(t, c.WarmReset(context.Background()))
}

func TestVMChassisUnsupportedOps(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	c := vmChassis{rm: resourcemanager.NewMockResourceManager(ctrl)}

	assert.ErrorIs(t, c.Identify(context.Background(), 1), hal.ErrNotSupported)
	_, err := c.IntrusionState(context.Background())
	assert.ErrorIs(t, err, hal.ErrNotSupported)
}

// expectSetBootDevice sets up a mock SetBootDevice expectation.
func expectSetBootDevice(
	t *testing.T,
	mockRM *resourcemanager.MockResourceManager,
	device resourcemanager.BootDevice,
	mode resourcemanager.BootMode,
	efiBoot *bool,
	returnErr error,
) {
	t.Helper()
	mockRM.EXPECT().
		SetBootDevice(gomock.Any(), device, gomock.AssignableToTypeOf(&resourcemanager.BootOptions{})).
		DoAndReturn(func(_ context.Context, _ resourcemanager.BootDevice, opts *resourcemanager.BootOptions) error {
			assert.Equal(t, mode, opts.Mode)
			if efiBoot == nil {
				assert.Nil(t, opts.EFIBoot)
			} else if assert.NotNil(t, opts.EFIBoot) {
				assert.Equal(t, *efiBoot, *opts.EFIBoot)
			}
			return returnErr
		})
}

func TestVMChassisSetBootFlags(t *testing.T) {
	efiBootTrue := true

	cases := []struct {
		name       string
		selector   types.BootDeviceSelector
		persist    bool
		efiBoot    *bool // nil means BIOSBootType=false (no explicit EFI request)
		device     resourcemanager.BootDevice
		expectCall bool // expect SetBootDevice
		clearOvrd  bool // expect ClearBootOverrides
		returnErr  error
	}{
		{
			name:       "PXE oneshot (default)",
			selector:   types.BootDeviceSelectorForcePXE,
			device:     resourcemanager.BootDevicePxe,
			expectCall: true,
		},
		{
			name:       "PXE persistent",
			selector:   types.BootDeviceSelectorForcePXE,
			persist:    true,
			device:     resourcemanager.BootDevicePxe,
			expectCall: true,
		},
		{
			name:       "PXE oneshot EFI",
			selector:   types.BootDeviceSelectorForcePXE,
			efiBoot:    &efiBootTrue,
			device:     resourcemanager.BootDevicePxe,
			expectCall: true,
		},
		{
			name:       "PXE persistent EFI",
			selector:   types.BootDeviceSelectorForcePXE,
			persist:    true,
			efiBoot:    &efiBootTrue,
			device:     resourcemanager.BootDevicePxe,
			expectCall: true,
		},
		{
			name:       "HDD oneshot",
			selector:   types.BootDeviceSelectorForceHardDrive,
			device:     resourcemanager.BootDeviceHdd,
			expectCall: true,
		},
		{
			name:       "HDD safe persistent",
			selector:   types.BootDeviceSelectorForceHardDriveSafe,
			persist:    true,
			device:     resourcemanager.BootDeviceHdd,
			expectCall: true,
		},
		{
			name:       "CDROM oneshot",
			selector:   types.BootDeviceSelectorForceCDROM,
			device:     resourcemanager.BootDeviceCd,
			expectCall: true,
		},
		{
			name:      "NoOverride clears overrides",
			selector:  types.BootDeviceSelectorNoOverride,
			clearOvrd: true,
		},
		{
			name:     "BIOS setup (unsupported, no-op)",
			selector: types.BootDeviceSelectorForceBIOSSetup,
		},
		{
			name:     "Diagnostic partition (unsupported, no-op)",
			selector: types.BootDeviceSelectorForceDiagnosticPartition,
		},
		{
			name:       "PXE SetBootDevice error propagates",
			selector:   types.BootDeviceSelectorForcePXE,
			device:     resourcemanager.BootDevicePxe,
			expectCall: true,
			returnErr:  assert.AnError,
		},
		{
			name:      "NoOverride ClearBootOverrides error propagates",
			selector:  types.BootDeviceSelectorNoOverride,
			clearOvrd: true,
			returnErr: assert.AnError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRM := resourcemanager.NewMockResourceManager(ctrl)

			if tc.clearOvrd {
				mockRM.EXPECT().ClearBootOverrides(gomock.Any()).Return(tc.returnErr)
			} else if tc.expectCall {
				expectedMode := resourcemanager.BootModeOneshot
				if tc.persist {
					expectedMode = resourcemanager.BootModePersistent
				}
				expectSetBootDevice(t, mockRM, tc.device, expectedMode, tc.efiBoot, tc.returnErr)
			}
			// Unsupported selectors: no mock calls expected.

			c := vmChassis{rm: mockRM}
			flags := &types.BootOptionParam_BootFlags{
				BootDeviceSelector: tc.selector,
				Persist:            tc.persist,
			}
			if tc.efiBoot != nil {
				flags.BIOSBootType = types.BIOSBootType(*tc.efiBoot)
			}
			err := c.SetBootFlags(context.Background(), flags)
			if tc.returnErr != nil {
				assert.ErrorIs(t, err, tc.returnErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestVMChassisGetBootFlags(t *testing.T) {
	efiBootTrue := true

	cases := []struct {
		name        string
		state       *resourcemanager.BootFlagsState
		stateErr    error
		expectErr   bool
		expectFlags *types.BootOptionParam_BootFlags
	}{
		{
			name: "PXE persistent BIOS",
			state: &resourcemanager.BootFlagsState{
				BootDevice:     resourcemanager.BootDevicePxe,
				Mode:           resourcemanager.BootModePersistent,
				EFIBoot:        false,
				OverrideActive: true,
			},
			expectFlags: &types.BootOptionParam_BootFlags{
				BootFlagsValid:     true,
				Persist:            true,
				BIOSBootType:       false,
				BootDeviceSelector: types.BootDeviceSelectorForcePXE,
			},
		},
		{
			name: "HDD oneshot EFI",
			state: &resourcemanager.BootFlagsState{
				BootDevice:     resourcemanager.BootDeviceHdd,
				Mode:           resourcemanager.BootModeOneshot,
				EFIBoot:        true,
				OverrideActive: true,
			},
			expectFlags: &types.BootOptionParam_BootFlags{
				BootFlagsValid:     true,
				Persist:            false,
				BIOSBootType:       true,
				BootDeviceSelector: types.BootDeviceSelectorForceHardDrive,
			},
		},
		{
			name: "CD oneshot BIOS",
			state: &resourcemanager.BootFlagsState{
				BootDevice:     resourcemanager.BootDeviceCd,
				Mode:           resourcemanager.BootModeOneshot,
				EFIBoot:        false,
				OverrideActive: true,
			},
			expectFlags: &types.BootOptionParam_BootFlags{
				BootFlagsValid:     true,
				Persist:            false,
				BIOSBootType:       false,
				BootDeviceSelector: types.BootDeviceSelectorForceCDROM,
			},
		},
		{
			name: "no override active shows NoOverride with BootFlagsValid=false",
			state: &resourcemanager.BootFlagsState{
				BootDevice:     resourcemanager.BootDeviceHdd,
				Mode:           resourcemanager.BootModePersistent,
				EFIBoot:        false,
				OverrideActive: false,
			},
			expectFlags: &types.BootOptionParam_BootFlags{
				BootFlagsValid:     false,
				Persist:            true,
				BIOSBootType:       false,
				BootDeviceSelector: types.BootDeviceSelectorNoOverride,
			},
		},
		{
			name:      "resource manager error is propagated",
			stateErr:  assert.AnError,
			expectErr: true,
		},
		{
			name:      "nil state returns ErrNotSupported",
			expectErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRM := resourcemanager.NewMockResourceManager(ctrl)
			c := vmChassis{rm: mockRM}

			mockRM.EXPECT().GetBootFlags(gomock.Any()).Return(tc.state, tc.stateErr)

			flags, err := c.GetBootFlags(context.Background())
			if tc.expectErr {
				if tc.stateErr != nil {
					assert.ErrorIs(t, err, tc.stateErr)
				} else {
					assert.Error(t, err)
				}
				assert.Nil(t, flags)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.expectFlags, flags)
		})
	}

	t.Run("EFI flag mapping", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRM := resourcemanager.NewMockResourceManager(ctrl)
		c := vmChassis{rm: mockRM}

		mockRM.EXPECT().GetBootFlags(gomock.Any()).Return(&resourcemanager.BootFlagsState{
			BootDevice:     resourcemanager.BootDevicePxe,
			Mode:           resourcemanager.BootModeOneshot,
			EFIBoot:        efiBootTrue,
			OverrideActive: true,
		}, nil)

		flags, err := c.GetBootFlags(context.Background())
		assert.NoError(t, err)
		assert.True(t, bool(flags.BIOSBootType), "BIOSBootType should be true when EFIBoot is true")
	})
}

func TestVMChassisSetBootFlagsNil(t *testing.T) {
	c := vmChassis{rm: resourcemanager.NewMockResourceManager(gomock.NewController(t))}
	assert.ErrorIs(t, c.SetBootFlags(context.Background(), nil), hal.ErrNotSupported)
}

// TestVMChassisBootInfoAckNoPersistence asserts SetBootInfoAcknowledge is
// accepted as a no-op and GetBootInfoAcknowledge always returns ErrNotSupported
// (→ 0x80). Like boot flags, the acknowledge is not persisted across pod
// restarts, so reporting "not supported" consistently avoids stale values.
func TestVMChassisBootInfoAckNoPersistence(t *testing.T) {
	c := vmChassis{rm: resourcemanager.NewMockResourceManager(gomock.NewController(t))}

	// Get before Set: not supported.
	_, err := c.GetBootInfoAcknowledge(context.Background())
	assert.ErrorIs(t, err, hal.ErrNotSupported)

	// Set is accepted as a no-op (spec §28.14 allows a no-op HAL).
	ack := &types.BootOptionParam_BootInfoAcknowledge{ByBIOSPOST: true, BySMS: true}
	assert.NoError(t, c.SetBootInfoAcknowledge(context.Background(), ack))

	// Get after Set: still not supported (not persisted).
	_, err = c.GetBootInfoAcknowledge(context.Background())
	assert.ErrorIs(t, err, hal.ErrNotSupported)
}

func TestVMChassisSetBootInfoAckNil(t *testing.T) {
	c := vmChassis{rm: resourcemanager.NewMockResourceManager(gomock.NewController(t))}
	assert.ErrorIs(t, c.SetBootInfoAcknowledge(context.Background(), nil), hal.ErrNotSupported)
}

// TestKubevirtBootDeviceMapping locks in the IPMI BootDeviceSelector → KubeVirt
// BootDevice translation table used by SetBootFlags.
func TestKubevirtBootDeviceMapping(t *testing.T) {
	cases := []struct {
		selector types.BootDeviceSelector
		device   resourcemanager.BootDevice
		expect   bool
	}{
		{types.BootDeviceSelectorForcePXE, resourcemanager.BootDevicePxe, true},
		{types.BootDeviceSelectorForceHardDrive, resourcemanager.BootDeviceHdd, true},
		{types.BootDeviceSelectorForceHardDriveSafe, resourcemanager.BootDeviceHdd, true},
		{types.BootDeviceSelectorForceCDROM, resourcemanager.BootDeviceCd, true},
		{types.BootDeviceSelectorNoOverride, "", false},
		{types.BootDeviceSelectorForceBIOSSetup, "", false},
		{types.BootDeviceSelectorForceDiagnosticPartition, "", false},
		{types.BootDeviceSelectorForceFloppy, "", false},
	}
	for _, tc := range cases {
		device, ok := kubevirtBootDevice(tc.selector)
		assert.Equal(t, tc.expect, ok, "selector %v", tc.selector)
		if ok {
			assert.Equal(t, tc.device, device, "selector %v", tc.selector)
		}
	}
}
