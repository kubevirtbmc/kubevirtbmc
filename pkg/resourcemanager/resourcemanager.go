package resourcemanager

type BootDevice string

const (
	BootDevicePxe BootDevice = "Pxe"
	BootDeviceHdd BootDevice = "Hdd"
	BootDeviceCd  BootDevice = "Cd"
)

type ResourceManager interface {
	GetComputerSystem() (ComputerSystemInterface, error)
	GetManager() (ManagerInterface, error)
	GetVirtualMedia() (VirtualMediaInterface, error)

	EjectMedia() error
	InsertMedia(string) error
	GetPowerStatus() (bool, error)
	PowerOn() error
	PowerOff() error
	ForcePowerOff() error
	PowerCycle() error
	ForcePowerCycle() error
	SetBootDevice(BootDevice) error
	GetSystemUUID() (string, error)
}
