package ipmi

import (
	"context"

	"github.com/bougou/go-ipmi/pkg/hal"
	ipmi "github.com/bougou/go-ipmi/pkg/types"
	"github.com/sirupsen/logrus"

	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

// noopHAL implements hal.HAL returning nil for every sub-interface except
// Chassis, which is backed by vmChassis so go-ipmi's typed chassis handlers
// (Chassis Control, Set/Get System Boot Options, Get Chassis Status) can drive
// the KubeVirt ResourceManager through the typed HAL contract.
type noopHAL struct {
	chassis hal.ChassisHAL
}

func (h noopHAL) Chassis() hal.ChassisHAL { return h.chassis }
func (noopHAL) Sensors() hal.SensorHAL    { return nil }
func (noopHAL) Storage() hal.StorageHAL   { return nil }
func (noopHAL) Network() hal.NetworkHAL   { return nil }
func (noopHAL) GPIO() hal.GPIOHAL         { return nil }
func (noopHAL) I2C() hal.I2CHAL           { return nil }
func (noopHAL) Close() error              { return nil }

// vmChassis implements hal.ChassisHAL by mapping the spec Table 28-3 chassis
// actions and §28.12 Set System Boot Options onto KubeVirt ResourceManager
// APIs. go-ipmi's built-in handlers dispatch through these methods; this struct
// carries only the KubeVirt-specific glue and holds no state.
//
// Action mapping (spec Table 28-3 → go-ipmi HAL method → KubeVirt API):
//
//	0x00 Power Down          → SetPower(false) → rm.ForcePowerOff()    (immediate, no OS notification)
//	0x01 Power Up            → SetPower(true)  → rm.PowerOn()
//	0x02 Power Cycle         → PowerCycle()    → rm.ForcePowerCycle()  (cuts power entirely)
//	0x03 Hard Reset          → ColdReset()     → rm.PowerCycle()       (asserts reset line, less brutal)
//	0x04 Diagnostic Interrupt → go-ipmi returns CodeParamOutOfRange (no HAL call)
//	0x05 ACPI Soft Shutdown  → WarmReset()     → rm.PowerOff()         (graceful stop)
//
// Per IPMI spec §28.3, Power Cycle (0x02) turns the system off then back on
// — cutting power entirely — which is the most disruptive reset and maps to the
// force variant (GracePeriodSeconds=0). Hard Reset (0x03) asserts the system
// reset line without power cycling, making it strictly less brutal; it maps to
// a graceful restart.
//
// Boot flags (spec §28.13 parameter 5) are not mirrored in BMC memory — the
// virtbmc pod is stateless and a restart would lose any in-memory copy. The
// authoritative boot state lives in the VM spec and status.bootOverride on
// the VirtualMachineBMC CR: SetBootFlags writes it via rm.SetBootDevice /
// rm.ClearBootOverrides, GetBootFlags reads it back via rm.GetBootFlags.
// GetBootInfoAcknowledge has no KubeVirt counterpart and returns
// hal.ErrNotSupported (→ 0x80, spec §28.13).
type vmChassis struct {
	rm resourcemanager.ResourceManager
}

func (c vmChassis) PowerState(ctx context.Context) (bool, error) {
	return c.rm.GetPowerStatus()
}

func (c vmChassis) SetPower(ctx context.Context, on bool) error {
	if on {
		logrus.Info("power on")
		return c.rm.PowerOn()
	}
	// Immediate power down (no OS notification), per IPMI spec §28.3
	// ControlPowerDown (0x00). Contrast with WarmReset (0x05 ACPI Soft
	// Shutdown), which is a graceful stop.
	logrus.Info("force power off")
	return c.rm.ForcePowerOff()
}

// PowerCycle maps IPMI Chassis Control Power Cycle (0x02) to a forceful restart
// (GracePeriodSeconds=0). Per IPMI spec §28.3, this turns the system off then
// back on — cutting power entirely — which is the most disruptive reset.
// Contrast with ColdReset (0x03 Hard Reset), which asserts the system reset
// line without power cycling and is strictly less brutal.
func (c vmChassis) PowerCycle(ctx context.Context) error {
	logrus.Info("power cycle (force)")
	return c.rm.ForcePowerCycle()
}

// ColdReset maps IPMI Hard Reset (0x03) to a graceful KubeVirt restart. Per
// IPMI spec §28.3, Hard Reset asserts the system reset line without power
// cycling, making it less disruptive than a full Power Cycle (0x02).
func (c vmChassis) ColdReset(ctx context.Context) error {
	logrus.Info("hard reset (graceful restart)")
	return c.rm.PowerCycle()
}

// WarmReset maps IPMI ACPI Soft Shutdown (0x05) to a graceful KubeVirt stop.
// go-ipmi dispatches ChassisControlSoftShutdown (0x05) here; per §28.3 that
// action is an ACPI soft-shutdown (graceful power-off), not a reboot.
func (c vmChassis) WarmReset(ctx context.Context) error {
	logrus.Info("acpi soft shutdown")
	return c.rm.PowerOff()
}

func (vmChassis) Identify(context.Context, uint8) error { return hal.ErrNotSupported }
func (vmChassis) IntrusionState(context.Context) (bool, error) {
	return false, hal.ErrNotSupported
}

// SetBootFlags commits boot flags (spec Table 28-6) to the KubeVirt VM spec.
// ForcePXE/ForceHardDrive/ForceCDROM map to rm.SetBootDevice with BootOptions
// carrying the persistence mode (Persist bit) and firmware type (BIOSBootType);
// NoOverride maps to rm.ClearBootOverrides. Other selectors (BIOS setup,
// diagnostic partition, floppy, …) are acknowledged without side effect.
//
// Deliberate spec deviation: BIOSBootType (data 1 bit 5) is only honored when
// set (1b = EFI). Per Table 28-14 the bit is always meaningful and 0b requests
// a PC-compatible (legacy) boot, but ipmitool has no legacy option — 0b is the
// default every plain `chassis bootdev` sends — so 0b cannot distinguish
// "explicit legacy request" from "unspecified". Treating 0b as a legacy
// request would make every plain bootdev on an EFI VM a firmware switch, which
// is far heavier than a boot-order override and almost never intended. EFI can
// be reverted to legacy through Redfish BootSourceOverrideMode=Legacy instead.
func (c vmChassis) SetBootFlags(_ context.Context, flags *ipmi.BootOptionParam_BootFlags) error {
	if flags == nil {
		return hal.ErrNotSupported
	}

	if flags.BootDeviceSelector == ipmi.BootDeviceSelectorNoOverride {
		return c.rm.ClearBootOverrides()
	}

	device, ok := kubevirtBootDevice(flags.BootDeviceSelector)
	if !ok {
		return nil
	}

	opts := &resourcemanager.BootOptions{}
	if flags.Persist {
		opts.Mode = resourcemanager.BootModePersistent
	} else {
		opts.Mode = resourcemanager.BootModeOneshot
	}
	if flags.BIOSBootType {
		efiBoot := true
		opts.EFIBoot = &efiBoot
	}

	return c.rm.SetBootDevice(device, opts)
}

// GetBootFlags reads back boot flags (device, firmware type, persistence)
// derived by the ResourceManager from the VM spec and status.bootOverride.
func (c vmChassis) GetBootFlags(_ context.Context) (*ipmi.BootOptionParam_BootFlags, error) {
	state, err := c.rm.GetBootFlags()
	if err != nil {
		return nil, hal.ErrNotSupported
	}
	if state == nil {
		return nil, hal.ErrNotSupported
	}

	flags := &ipmi.BootOptionParam_BootFlags{
		BootFlagsValid: state.OverrideActive,
		Persist:        state.Mode == resourcemanager.BootModePersistent,
		BIOSBootType:   ipmi.BIOSBootType(state.EFIBoot),
	}

	if state.OverrideActive {
		switch state.BootDevice {
		case resourcemanager.BootDevicePxe:
			flags.BootDeviceSelector = ipmi.BootDeviceSelectorForcePXE
		case resourcemanager.BootDeviceHdd:
			flags.BootDeviceSelector = ipmi.BootDeviceSelectorForceHardDrive
		case resourcemanager.BootDeviceCd:
			flags.BootDeviceSelector = ipmi.BootDeviceSelectorForceCDROM
		default:
			flags.BootDeviceSelector = ipmi.BootDeviceSelectorNoOverride
		}
	} else {
		flags.BootDeviceSelector = ipmi.BootDeviceSelectorNoOverride
	}

	return flags, nil
}

// SetBootInfoAcknowledge is accepted as a no-op (spec §28.14 explicitly allows
// a no-op HAL). KubeVirt has no corresponding concept.
func (vmChassis) SetBootInfoAcknowledge(_ context.Context, ack *ipmi.BootOptionParam_BootInfoAcknowledge) error {
	if ack == nil {
		return hal.ErrNotSupported
	}
	return nil
}

// GetBootInfoAcknowledge always returns ErrNotSupported (→ 0x80); the value is
// not persisted anywhere.
func (vmChassis) GetBootInfoAcknowledge(context.Context) (*ipmi.BootOptionParam_BootInfoAcknowledge, error) {
	return nil, hal.ErrNotSupported
}

// kubevirtBootDevice maps an IPMI BootDeviceSelector (spec Table 28-6 bits 5:2
// of byte 2) to a KubeVirt ResourceManager BootDevice. The KubeVirt boot-order
// patch only distinguishes the three categories ipmitool exposes (pxe/disk/
// cdrom); other selectors (diagnostic partition, BIOS setup, floppy, remote
// media, no-override) are acknowledged without a KubeVirt side effect.
func kubevirtBootDevice(selector ipmi.BootDeviceSelector) (resourcemanager.BootDevice, bool) {
	switch selector {
	case ipmi.BootDeviceSelectorForcePXE:
		return resourcemanager.BootDevicePxe, true
	case ipmi.BootDeviceSelectorForceHardDrive, ipmi.BootDeviceSelectorForceHardDriveSafe:
		return resourcemanager.BootDeviceHdd, true
	case ipmi.BootDeviceSelectorForceCDROM:
		return resourcemanager.BootDeviceCd, true
	default:
		return "", false
	}
}
