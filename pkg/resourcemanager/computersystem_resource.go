package resourcemanager

import (
	"fmt"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/util"
)

type ComputerSystemInterface interface {
	OdataInterface

	Id() string
	GetPowerState() server.ResourcePowerState
	SetPowerState(powerState server.ResourcePowerState)
	SetBootOverride(BootDevice, OverrideMode)
	SetFirmwareMode(FirmwareMode)
	ClearBootOverride()
}

var (
	overrideModeToRedfish = map[OverrideMode]server.ComputerSystemV1220BootSourceOverrideEnabled{
		OverrideModeDisabled:   server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_DISABLED,
		OverrideModeOnce:       server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_ONCE,
		OverrideModeContinuous: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_CONTINUOUS,
	}
	firmwareModeToRedfish = map[FirmwareMode]server.ComputerSystemV1220BootSourceOverrideMode{
		FirmwareModeLegacy: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEMODE_LEGACY,
		FirmwareModeUEFI:   server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEMODE_UEFI,
	}
)

// BootDeviceToRedfishTarget maps a BootDevice to the Redfish BootSource enum.
func BootDeviceToRedfishTarget(d BootDevice) server.ComputerSystemBootSource {
	return bootSourceMap[d]
}

// EFIBootToRedfishMode maps the EFI firmware flag to the Redfish override mode.
func EFIBootToRedfishMode(efi bool) server.ComputerSystemV1220BootSourceOverrideMode {
	if efi {
		return server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEMODE_UEFI
	}
	return server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEMODE_LEGACY
}

type ComputerSystemAdapter struct {
	computerSystem *server.ComputerSystemV1220ComputerSystem
}

func NewComputerSystem(id, name string, powerState server.ResourcePowerState) *ComputerSystemAdapter {
	generatedComputerSystem := &server.ComputerSystemV1220ComputerSystem{
		OdataContext: "/redfish/v1/$metadata#ComputerSystem.ComputerSystem",
		OdataId:      fmt.Sprintf("/redfish/v1/Systems/%s", id),
		OdataType:    "#ComputerSystem.v1_22_0.ComputerSystem",
		Description:  "Computer System",
		Name:         name,
		Id:           id,
		UUID:         "00000000-0000-0000-0000-000000000000",
		AssetTag:     util.Ptr(""),
		IndicatorLED: server.COMPUTERSYSTEMV1220INDICATORLED_UNKNOWN,
		Manufacturer: util.Ptr("KubeVirt"),
		Model:        util.Ptr("KubeVirt"),
		PartNumber:   util.Ptr(""),
		SerialNumber: util.Ptr("000000000000"),
		SKU:          util.Ptr(""),
		Status:       server.ResourceStatus{},
		SystemType:   server.COMPUTERSYSTEMV1220SYSTEMTYPE_VIRTUAL,
		Links:        server.ComputerSystemV1220Links{},
		PowerState:   powerState,
		Actions: server.ComputerSystemV1220Actions{
			ComputerSystemReset: server.ComputerSystemV1220Reset{
				Target: fmt.Sprintf("/redfish/v1/Systems/%s/Actions/ComputerSystem.Reset", id),
				Title:  "Reset",
				ResetTypeRedfishAllowableValues: []server.ResourceResetType{
					server.RESOURCERESETTYPE_ON,
					server.RESOURCERESETTYPE_FORCE_OFF,
					server.RESOURCERESETTYPE_GRACEFUL_SHUTDOWN,
					server.RESOURCERESETTYPE_GRACEFUL_RESTART,
					server.RESOURCERESETTYPE_FORCE_RESTART,
				},
			},
		},
		Boot: server.ComputerSystemV1220Boot{
			BootSourceOverrideEnabled: server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_DISABLED,
			BootSourceOverrideMode:    server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEMODE_LEGACY,
			BootSourceOverrideTarget:  server.COMPUTERSYSTEMBOOTSOURCE_HDD,
		},
		OperatingSystem: fmt.Sprintf("/redfish/v1/Systems/%s/OperatingSystem", id),
		VirtualMedia: server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Systems/%s/VirtualMedia", id),
		},
		HostWatchdogTimer: server.ComputerSystemV1220WatchdogTimer{
			FunctionEnabled: util.Ptr(false),
		},
		MemorySummary: server.ComputerSystemV1220MemorySummary{
			Status:               server.ResourceStatus{},
			TotalSystemMemoryGiB: util.Ptr(float32(0)),
		},
		NetworkInterfaces: server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Systems/%s/NetworkInterfaces", id),
		},
		ProcessorSummary: server.ComputerSystemV1220ProcessorSummary{
			Status: server.ResourceStatus{},
			Count:  util.Ptr(int64(0)),
		},
		SimpleStorage: server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Systems/%s/SimpleStorage", id),
		},
		Storage: server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Systems/%s/Storage", id),
		},
	}

	return &ComputerSystemAdapter{computerSystem: generatedComputerSystem}
}

func (a *ComputerSystemAdapter) Id() string {
	return a.computerSystem.Id
}

func (a *ComputerSystemAdapter) OdataId() string {
	return a.computerSystem.OdataId
}

func (a *ComputerSystemAdapter) Manage(resource OdataInterface) error {
	panic("implement me")
}

func (a *ComputerSystemAdapter) ManagedBy(resource OdataInterface) error {
	a.computerSystem.Links.ManagedBy = append(a.computerSystem.Links.ManagedBy, server.OdataV4IdRef{
		OdataId: resource.OdataId(),
	})

	return nil
}

func (a *ComputerSystemAdapter) ComputerSystem() *server.ComputerSystemV1220ComputerSystem {
	return a.computerSystem
}

func (a *ComputerSystemAdapter) GetPowerState() server.ResourcePowerState {
	return a.computerSystem.PowerState
}

func (a *ComputerSystemAdapter) SetPowerState(powerState server.ResourcePowerState) {
	a.computerSystem.PowerState = powerState
}

func (a *ComputerSystemAdapter) SetBootOverride(device BootDevice, mode OverrideMode) {
	a.computerSystem.Boot.BootSourceOverrideTarget = bootSourceMap[device]
	a.computerSystem.Boot.BootSourceOverrideEnabled = overrideModeToRedfish[mode]
}

func (a *ComputerSystemAdapter) SetFirmwareMode(mode FirmwareMode) {
	a.computerSystem.Boot.BootSourceOverrideMode = firmwareModeToRedfish[mode]
}

func (a *ComputerSystemAdapter) ClearBootOverride() {
	a.computerSystem.Boot.BootSourceOverrideEnabled = server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_DISABLED
}
