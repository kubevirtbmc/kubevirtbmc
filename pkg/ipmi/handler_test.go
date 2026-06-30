package ipmi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/bougou/go-ipmi/pkg/hal"
	ipmi "github.com/bougou/go-ipmi/pkg/types"

	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

// TestVMChassisPowerStateDelegates verifies the HAL chassis adapter feeds
// go-ipmi's built-in Get Chassis Status handler with the VM power state.
func TestVMChassisPowerStateDelegates(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	mockRM.EXPECT().GetPowerStatus().Return(true, nil)

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
	mockRM.EXPECT().PowerOn().Return(nil)
	mockRM.EXPECT().ForcePowerOff().Return(nil)

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
	mockRM.EXPECT().ForcePowerCycle().Return(nil)

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
	mockRM.EXPECT().PowerCycle().Return(nil)

	c := vmChassis{rm: mockRM}
	assert.NoError(t, c.ColdReset(context.Background()))
}

// TestVMChassisWarmResetMapsToPowerOff asserts ACPI Soft Shutdown (0x05),
// which go-ipmi dispatches to WarmReset, maps to a graceful KubeVirt stop.
func TestVMChassisWarmResetMapsToPowerOff(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	mockRM.EXPECT().PowerOff().Return(nil)

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

// TestVMChassisSetBootFlags asserts the typed BootFlags payload drives a
// KubeVirt SetBootDevice call for the recognised selectors. The flags are not
// persisted (see vmChassis doc), so no Get round-trip is asserted here.
func TestVMChassisSetBootFlags(t *testing.T) {
	cases := []struct {
		name     string
		selector ipmi.BootDeviceSelector
		device   resourcemanager.BootDevice
		expect   bool
	}{
		{"PXE", ipmi.BootDeviceSelectorForcePXE, resourcemanager.BootDevicePxe, true},
		{"HDD", ipmi.BootDeviceSelectorForceHardDrive, resourcemanager.BootDeviceHdd, true},
		{"HDD safe", ipmi.BootDeviceSelectorForceHardDriveSafe, resourcemanager.BootDeviceHdd, true},
		{"CDROM", ipmi.BootDeviceSelectorForceCDROM, resourcemanager.BootDeviceCd, true},
		{"no override", ipmi.BootDeviceSelectorNoOverride, "", false},
		{"BIOS setup", ipmi.BootDeviceSelectorForceBIOSSetup, "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRM := resourcemanager.NewMockResourceManager(ctrl)
			if tc.expect {
				mockRM.EXPECT().SetBootDevice(tc.device).Return(nil)
			}

			c := vmChassis{rm: mockRM}
			flags := &ipmi.BootOptionParam_BootFlags{BootDeviceSelector: tc.selector}
			assert.NoError(t, c.SetBootFlags(context.Background(), flags))
		})
	}
}

// TestVMChassisSetBootFlagsPropagatesError asserts a KubeVirt patch failure
// surfaces from SetBootFlags so go-ipmi maps it to CodeUnspecifiedError.
func TestVMChassisSetBootFlagsPropagatesError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	mockRM.EXPECT().SetBootDevice(resourcemanager.BootDevicePxe).
		Return(assert.AnError)

	c := vmChassis{rm: mockRM}
	err := c.SetBootFlags(context.Background(),
		&ipmi.BootOptionParam_BootFlags{BootDeviceSelector: ipmi.BootDeviceSelectorForcePXE})
	assert.ErrorIs(t, err, assert.AnError)
}

// TestVMChassisGetBootFlagsAlwaysNotSupported asserts GetBootFlags always
// returns ErrNotSupported (→ 0x80), both before and after a SetBootFlags. The
// virtual BMC does not persist boot flags (a pod restart would lose any
// in-memory copy and return values inconsistent with the VM spec bootOrder),
// so it consistently reports "parameter not supported" rather than stale state.
func TestVMChassisGetBootFlagsAlwaysNotSupported(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	c := vmChassis{rm: mockRM}

	// Before any Set.
	_, err := c.GetBootFlags(context.Background())
	assert.ErrorIs(t, err, hal.ErrNotSupported)

	// After a Set: SetBootDevice is driven (persistent VM spec change), but
	// GetBootFlags still reports not-supported — the authoritative state lives
	// in the VM spec, not in the BMC's boot-flags register.
	mockRM.EXPECT().SetBootDevice(resourcemanager.BootDevicePxe).Return(nil)
	assert.NoError(t, c.SetBootFlags(context.Background(),
		&ipmi.BootOptionParam_BootFlags{BootDeviceSelector: ipmi.BootDeviceSelectorForcePXE}))
	_, err = c.GetBootFlags(context.Background())
	assert.ErrorIs(t, err, hal.ErrNotSupported)
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
	ack := &ipmi.BootOptionParam_BootInfoAcknowledge{ByBIOSPOST: true, BySMS: true}
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
		selector ipmi.BootDeviceSelector
		device   resourcemanager.BootDevice
		expect   bool
	}{
		{ipmi.BootDeviceSelectorForcePXE, resourcemanager.BootDevicePxe, true},
		{ipmi.BootDeviceSelectorForceHardDrive, resourcemanager.BootDeviceHdd, true},
		{ipmi.BootDeviceSelectorForceHardDriveSafe, resourcemanager.BootDeviceHdd, true},
		{ipmi.BootDeviceSelectorForceCDROM, resourcemanager.BootDeviceCd, true},
		{ipmi.BootDeviceSelectorNoOverride, "", false},
		{ipmi.BootDeviceSelectorForceBIOSSetup, "", false},
		{ipmi.BootDeviceSelectorForceDiagnosticPartition, "", false},
		{ipmi.BootDeviceSelectorForceFloppy, "", false},
	}
	for _, tc := range cases {
		device, ok := kubevirtBootDevice(tc.selector)
		assert.Equal(t, tc.expect, ok, "selector %v", tc.selector)
		if ok {
			assert.Equal(t, tc.device, device, "selector %v", tc.selector)
		}
	}
}
