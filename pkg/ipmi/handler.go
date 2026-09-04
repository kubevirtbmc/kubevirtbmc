package ipmi

import (
	"context"
	"sort"
	"sync"

	"github.com/bougou/go-ipmi/pkg/hal"
	"github.com/bougou/go-ipmi/pkg/types"

	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

// noopHAL implements hal.HAL returning nil for every sub-interface except
// Chassis (vmChassis -> ResourceManager) and Storage (in-memory FRU/SDR seed
// for ipmitool fru list). Other sub-interfaces stay nil.
type noopHAL struct {
	chassis hal.ChassisHAL
	storage hal.StorageHAL
}

func (h noopHAL) Chassis() hal.ChassisHAL { return h.chassis }
func (noopHAL) Sensors() hal.SensorHAL    { return nil }
func (h noopHAL) Storage() hal.StorageHAL { return h.storage }
func (noopHAL) Network() hal.NetworkHAL   { return nil }
func (noopHAL) Console() hal.ConsoleHAL   { return nil }
func (noopHAL) GPIO() hal.GPIOHAL         { return nil }
func (noopHAL) I2C() hal.I2CHAL           { return nil }
func (noopHAL) Close() error              { return nil }

// memoryStorage is an in-memory [hal.StorageHAL] for the virtbmc FRU/SDR seed.
type memoryStorage struct {
	mu  sync.RWMutex
	fru map[uint8][]byte
	sdr map[uint16][]byte
}

func newMemoryStorage() *memoryStorage {
	return &memoryStorage{
		fru: map[uint8][]byte{},
		sdr: map[uint16][]byte{},
	}
}

func (s *memoryStorage) FRU() hal.FRUStore { return (*memoryFRUStore)(s) }
func (s *memoryStorage) SDR() hal.SDRStore { return (*memorySDRStore)(s) }

type memoryFRUStore memoryStorage

func (f *memoryFRUStore) Read(_ context.Context, deviceID uint8) ([]byte, error) {
	s := (*memoryStorage)(f)
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.fru[deviceID]
	if !ok {
		return nil, hal.ErrNotFound
	}
	return append([]byte(nil), v...), nil
}

func (f *memoryFRUStore) Write(_ context.Context, deviceID uint8, data []byte) error {
	s := (*memoryStorage)(f)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fru[deviceID] = append([]byte(nil), data...)
	return nil
}

func (f *memoryFRUStore) Delete(_ context.Context, deviceID uint8) error {
	s := (*memoryStorage)(f)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.fru, deviceID)
	return nil
}

func (f *memoryFRUStore) DeviceIDs(_ context.Context) ([]uint8, error) {
	s := (*memoryStorage)(f)
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]uint8, 0, len(s.fru))
	for id := range s.fru {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

type memorySDRStore memoryStorage

func (d *memorySDRStore) Read(_ context.Context, recordID uint16) ([]byte, error) {
	s := (*memoryStorage)(d)
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.sdr[recordID]
	if !ok {
		return nil, hal.ErrNotFound
	}
	return append([]byte(nil), v...), nil
}

func (d *memorySDRStore) Write(_ context.Context, recordID uint16, data []byte) error {
	s := (*memoryStorage)(d)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sdr[recordID] = append([]byte(nil), data...)
	return nil
}

func (d *memorySDRStore) Delete(_ context.Context, recordID uint16) error {
	s := (*memoryStorage)(d)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sdr, recordID)
	return nil
}

func (d *memorySDRStore) RecordIDs(_ context.Context) ([]uint16, error) {
	s := (*memoryStorage)(d)
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]uint16, 0, len(s.sdr))
	for id := range s.sdr {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

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
	return c.rm.GetPowerStatus(ctx)
}

func (c vmChassis) SetPower(ctx context.Context, on bool) error {
	if on {
		return c.rm.PowerOn(ctx)
	}
	// Immediate power down (no OS notification), per IPMI spec §28.3
	// ControlPowerDown (0x00). Contrast with WarmReset (0x05 ACPI Soft
	// Shutdown), which is a graceful stop.
	return c.rm.ForcePowerOff(ctx)
}

// PowerCycle maps IPMI Chassis Control Power Cycle (0x02) to a forceful restart
// (GracePeriodSeconds=0). Per IPMI spec §28.3, this turns the system off then
// back on — cutting power entirely — which is the most disruptive reset.
// Contrast with ColdReset (0x03 Hard Reset), which asserts the system reset
// line without power cycling and is strictly less brutal.
func (c vmChassis) PowerCycle(ctx context.Context) error {
	return c.rm.ForcePowerCycle(ctx)
}

// ColdReset maps IPMI Hard Reset (0x03) to a graceful KubeVirt restart. Per
// IPMI spec §28.3, Hard Reset asserts the system reset line without power
// cycling, making it less disruptive than a full Power Cycle (0x02).
func (c vmChassis) ColdReset(ctx context.Context) error {
	return c.rm.PowerCycle(ctx)
}

// WarmReset maps IPMI ACPI Soft Shutdown (0x05) to a graceful KubeVirt stop.
// go-ipmi dispatches ChassisControlSoftShutdown (0x05) here; per §28.3 that
// action is an ACPI soft-shutdown (graceful power-off), not a reboot.
func (c vmChassis) WarmReset(ctx context.Context) error {
	return c.rm.PowerOff(ctx)
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
func (c vmChassis) SetBootFlags(ctx context.Context, flags *types.BootOptionParam_BootFlags) error {
	if flags == nil {
		return hal.ErrNotSupported
	}

	if flags.BootDeviceSelector == types.BootDeviceSelectorNoOverride {
		return c.rm.ClearBootOverrides(ctx)
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

	return c.rm.SetBootDevice(ctx, device, opts)
}

// GetBootFlags reads back boot flags (device, firmware type, persistence)
// derived by the ResourceManager from the VM spec and status.bootOverride.
func (c vmChassis) GetBootFlags(ctx context.Context) (*types.BootOptionParam_BootFlags, error) {
	state, err := c.rm.GetBootFlags(ctx)
	if err != nil {
		return nil, hal.ErrNotSupported
	}
	if state == nil {
		return nil, hal.ErrNotSupported
	}

	flags := &types.BootOptionParam_BootFlags{
		BootFlagsValid: state.OverrideActive,
		Persist:        state.Mode == resourcemanager.BootModePersistent,
		BIOSBootType:   types.BIOSBootType(state.EFIBoot),
	}

	if state.OverrideActive {
		switch state.BootDevice {
		case resourcemanager.BootDevicePxe:
			flags.BootDeviceSelector = types.BootDeviceSelectorForcePXE
		case resourcemanager.BootDeviceHdd:
			flags.BootDeviceSelector = types.BootDeviceSelectorForceHardDrive
		case resourcemanager.BootDeviceCd:
			flags.BootDeviceSelector = types.BootDeviceSelectorForceCDROM
		default:
			flags.BootDeviceSelector = types.BootDeviceSelectorNoOverride
		}
	} else {
		flags.BootDeviceSelector = types.BootDeviceSelectorNoOverride
	}

	return flags, nil
}

// SetBootInfoAcknowledge is accepted as a no-op (spec §28.14 explicitly allows
// a no-op HAL). KubeVirt has no corresponding concept.
func (vmChassis) SetBootInfoAcknowledge(_ context.Context, ack *types.BootOptionParam_BootInfoAcknowledge) error {
	if ack == nil {
		return hal.ErrNotSupported
	}
	return nil
}

// GetBootInfoAcknowledge always returns ErrNotSupported (→ 0x80); the value is
// not persisted anywhere.
func (vmChassis) GetBootInfoAcknowledge(context.Context) (*types.BootOptionParam_BootInfoAcknowledge, error) {
	return nil, hal.ErrNotSupported
}

// kubevirtBootDevice maps an IPMI BootDeviceSelector (spec Table 28-6 bits 5:2
// of byte 2) to a KubeVirt ResourceManager BootDevice. The KubeVirt boot-order
// patch only distinguishes the three categories ipmitool exposes (pxe/disk/
// cdrom); other selectors (diagnostic partition, BIOS setup, floppy, remote
// media, no-override) are acknowledged without a KubeVirt side effect.
func kubevirtBootDevice(selector types.BootDeviceSelector) (resourcemanager.BootDevice, bool) {
	switch selector {
	case types.BootDeviceSelectorForcePXE:
		return resourcemanager.BootDevicePxe, true
	case types.BootDeviceSelectorForceHardDrive, types.BootDeviceSelectorForceHardDriveSafe:
		return resourcemanager.BootDeviceHdd, true
	case types.BootDeviceSelectorForceCDROM:
		return resourcemanager.BootDeviceCd, true
	default:
		return "", false
	}
}
