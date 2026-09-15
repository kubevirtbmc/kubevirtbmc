package resourcemanager

import (
	"context"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
)

type BootDevice string

const (
	BootDevicePxe  BootDevice = "Pxe"
	BootDeviceHdd  BootDevice = "Hdd"
	BootDeviceCd   BootDevice = "Cd"
	BootDeviceNone BootDevice = "None"
)

// BootMode represents the persistence mode of a boot device override.
type BootMode string

const (
	BootModePersistent BootMode = "Persistent"
	BootModeOneshot    BootMode = "Oneshot"
)

// BootOptions carries optional parameters for SetBootDevice.
type BootOptions struct {
	Mode    BootMode
	EFIBoot *bool // nil = don't change firmware; true = EFI; false = Legacy BIOS
}

// OverrideMode represents the boot source override enablement state.
type OverrideMode string

const (
	OverrideModeDisabled   OverrideMode = "Disabled"
	OverrideModeOnce       OverrideMode = "Once"
	OverrideModeContinuous OverrideMode = "Continuous"
)

// FirmwareMode represents the boot source override firmware mode.
type FirmwareMode string

const (
	FirmwareModeLegacy FirmwareMode = "Legacy"
	FirmwareModeUEFI   FirmwareMode = "UEFI"
)

// BootFlagsState holds the current effective boot flags read from the VM template spec.
type BootFlagsState struct {
	BootDevice     BootDevice
	Mode           BootMode // Persist bit for IPMI; Oneshot/Persistent
	EFIBoot        bool
	OverrideActive bool // true when status.bootOverride exists on the VirtualMachineBMC CR
}

type ResourceManager interface {
	GetBootFlags(ctx context.Context) (*BootFlagsState, error)
	GetBootOverride(ctx context.Context) (*bmcv1.BootOverrideStatus, error)
	GetComputerSystem(ctx context.Context) (ComputerSystemInterface, error)
	GetManager(ctx context.Context) (ManagerInterface, error)
	GetVirtualMedia(ctx context.Context) (VirtualMediaInterface, error)

	EjectMedia(ctx context.Context) error
	InsertMedia(ctx context.Context, image string) error
	GetPowerStatus(ctx context.Context) (bool, error)
	PowerOn(ctx context.Context) error
	PowerOff(ctx context.Context) error
	ForcePowerOff(ctx context.Context) error
	PowerCycle(ctx context.Context) error
	ForcePowerCycle(ctx context.Context) error
	SetBootDevice(ctx context.Context, device BootDevice, opts *BootOptions) error
	GetSystemUUID(ctx context.Context) (string, error)
	GetSystemSerial(ctx context.Context) (string, error)
	SetFirmwareMode(ctx context.Context, mode FirmwareMode) error
	ClearBootOverrides(ctx context.Context) error
}
