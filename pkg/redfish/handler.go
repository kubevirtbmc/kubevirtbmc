package redfish

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
	"kubevirt.io/kubevirtbmc/pkg/accesslog"
	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
	"kubevirt.io/kubevirtbmc/pkg/session"
	"kubevirt.io/kubevirtbmc/pkg/util"
)

type handler struct {
	rm resourcemanager.ResourceManager

	bmcUser     string
	bmcPassword string
}

func NewHandler(bmcUser string, bmcPassword string, resourceManager resourcemanager.ResourceManager) *handler {
	return &handler{
		rm:          resourceManager,
		bmcUser:     bmcUser,
		bmcPassword: bmcPassword,
	}
}

func (h *handler) Authenticate(username, password *string) (string, string, error) {
	var id, token string
	if username == nil || password == nil {
		return id, token, fmt.Errorf("username and password must be provided")
	}

	if *username != h.bmcUser || *password != h.bmcPassword {
		return id, token, fmt.Errorf("invalid username or password")
	}

	id = uuid.New().String()
	tokenInfo := session.NewTokenInfo(id, *username)
	token = session.AddToken(tokenInfo)

	return id, token, nil
}

func (h *handler) GetSession(sessionID string) (string, string, error) {
	var id, username string
	tokenInfo, exists := session.GetTokenFromSessionID(sessionID)
	if !exists {
		return id, username, fmt.Errorf("session not found")
	}
	return tokenInfo.ID, tokenInfo.Username, nil
}

func (h *handler) GetSessionCollection() *server.SessionCollectionSessionCollection {
	tokens := session.ListTokens()
	members := make([]server.OdataV4IdRef, 0, len(tokens))
	for _, t := range tokens {
		members = append(members, server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/SessionService/Sessions/%s", t.ID),
		})
	}
	return &server.SessionCollectionSessionCollection{
		OdataContext:      "/redfish/v1/$metadata#SessionCollection.SessionCollection",
		OdataId:           "/redfish/v1/SessionService/Sessions",
		OdataType:         "#SessionCollection.SessionCollection",
		Description:       "Session Collection",
		Name:              "Session Collection",
		Members:           members,
		MembersodataCount: int64(len(members)),
	}
}

func (h *handler) DeleteSession(sessionID string) {
	session.RemoveTokenBySessionID(sessionID)
}

func (h *handler) GetServiceRoot() *server.ServiceRootV1161ServiceRoot {
	return &server.ServiceRootV1161ServiceRoot{
		OdataContext:   "/redfish/v1/$metadata#ServiceRoot.ServiceRoot",
		OdataId:        "/redfish/v1",
		OdataType:      "#ServiceRoot.v1_16_1.ServiceRoot",
		Description:    "ServiceRoot",
		Name:           "ServiceRoot",
		RedfishVersion: "1.16.1",
		UUID:           util.Ptr("00000000-0000-0000-0000-000000000000"),
		Chassis: server.OdataV4IdRef{
			OdataId: "/redfish/v1/Chassis",
		},
		Managers: server.OdataV4IdRef{
			OdataId: "/redfish/v1/Managers",
		},
		Registries: server.OdataV4IdRef{
			OdataId: "/redfish/v1/Registries",
		},
		SessionService: server.OdataV4IdRef{
			OdataId: "/redfish/v1/SessionService",
		},
		Systems: server.OdataV4IdRef{
			OdataId: "/redfish/v1/Systems",
		},
		Tasks: server.OdataV4IdRef{
			OdataId: "/redfish/v1/Tasks",
		},
		AccountService: server.OdataV4IdRef{
			OdataId: "/redfish/v1/AccountService",
		},
		EventService: server.OdataV4IdRef{
			OdataId: "/redfish/v1/EventService",
		},
		TelemetryService: server.OdataV4IdRef{
			OdataId: "/redfish/v1/TelemetryService",
		},
		UpdateService: server.OdataV4IdRef{
			OdataId: "/redfish/v1/UpdateService",
		},
		CompositionService: server.OdataV4IdRef{
			OdataId: "/redfish/v1/CompositionService",
		},
		ProtocolFeaturesSupported: server.ServiceRootV1161ProtocolFeaturesSupported{},
		Links: server.ServiceRootV1161Links{
			ManagerProvidingService: server.OdataV4IdRef{
				OdataId: "/redfish/v1/Managers/BMC",
			},
			Oem: map[string]interface{}{},
			Sessions: server.OdataV4IdRef{
				OdataId: "/redfish/v1/SessionService/Sessions",
			},
		},
	}
}

func (h *handler) GetManagerCollection() *server.ManagerCollectionManagerCollection {
	return &server.ManagerCollectionManagerCollection{
		OdataContext: "/redfish/v1/$metadata#ManagerCollection.ManagerCollection",
		OdataId:      "/redfish/v1/Managers",
		OdataType:    "#ManagerCollection.ManagerCollection",
		Description:  "Manager Collection",
		Name:         "Manager Collection",
		Members: []server.OdataV4IdRef{
			{
				OdataId: "/redfish/v1/Managers/BMC",
			},
		},
	}
}

func (h *handler) GetManager(ctx context.Context) (*server.ManagerV1190Manager, error) {
	manager, err := h.rm.GetManager(ctx)
	if err != nil {
		return nil, err
	}

	adapter, ok := manager.(*resourcemanager.ManagerAdapter)
	if !ok {
		return nil, fmt.Errorf("manager is not a *resourcemanager.ManagerAdapter (got %T)", manager)
	}

	return adapter.Manager(), nil
}

func (h *handler) GetVirtualMediaCollection() *server.VirtualMediaCollectionVirtualMediaCollection {
	return &server.VirtualMediaCollectionVirtualMediaCollection{
		OdataContext: "/redfish/v1/$metadata#VirtualMediaCollection.VirtualMediaCollection",
		OdataId:      "/redfish/v1/Managers/BMC/VirtualMedia",
		OdataType:    "#VirtualMediaCollection.VirtualMediaCollection",
		Description:  "Virtual Media Collection",
		Name:         "Virtual Media Collection",
		Members: []server.OdataV4IdRef{
			{
				OdataId: "/redfish/v1/Managers/BMC/VirtualMedia/CD1",
			},
		},
		MembersodataCount: 1,
	}
}

func (h *handler) GetVirtualMedia(ctx context.Context) (*server.VirtualMediaV163VirtualMedia, error) {
	virtualMedia, err := h.rm.GetVirtualMedia(ctx)
	if err != nil {
		return nil, err
	}

	adapter, ok := virtualMedia.(*resourcemanager.VirtualMediaAdapter)
	if !ok {
		return nil, fmt.Errorf("virtualMedia is not a *resourcemanager.VirtualMediaAdapter (got %T)", virtualMedia)
	}

	return adapter.VirtualMedia(), nil
}

func (h *handler) VirtualMediaEject(ctx context.Context) error {
	accesslog.Record(ctx, logrus.Fields{"action": "eject_media"})
	return h.rm.EjectMedia(ctx)
}

func (h *handler) VirtualMediaInsert(ctx context.Context, image string) error {
	accesslog.Record(ctx, logrus.Fields{"action": "insert_media", "image": image})
	return h.rm.InsertMedia(ctx, image)
}

func (h *handler) GetComputerSystemCollection() *server.ComputerSystemCollectionComputerSystemCollection {
	return &server.ComputerSystemCollectionComputerSystemCollection{
		OdataContext: "/redfish/v1/$metadata#ComputerSystemCollection.ComputerSystemCollection",
		OdataId:      "/redfish/v1/Systems",
		OdataType:    "#ComputerSystemCollection.ComputerSystemCollection",
		Description:  "Computer System Collection",
		Name:         "Computer System Collection",
		Members: []server.OdataV4IdRef{
			{
				OdataId: "/redfish/v1/Systems/1",
			},
		},
	}
}

func (h *handler) GetComputerSystem(ctx context.Context) (*server.ComputerSystemV1220ComputerSystem, error) {
	computerSystem, err := h.rm.GetComputerSystem(ctx)
	if err != nil {
		return nil, err
	}

	adapter, ok := computerSystem.(*resourcemanager.ComputerSystemAdapter)
	if !ok {
		return nil, fmt.Errorf("computerSystem is not a *resourcemanager.ComputerSystemAdapter (got %T)", computerSystem)
	}

	cs := adapter.ComputerSystem()

	// GetBootFlags (VM spec + status.bootOverride) is authoritative; the
	// in-memory ComputerSystem model is lost on pod restart.
	if flags, err := h.rm.GetBootFlags(ctx); err == nil && flags != nil {
		cs.Boot.BootSourceOverrideTarget = resourcemanager.BootDeviceToRedfishTarget(flags.BootDevice)
		cs.Boot.BootSourceOverrideMode = resourcemanager.EFIBootToRedfishMode(flags.EFIBoot)
		if flags.OverrideActive {
			if flags.Mode == resourcemanager.BootModeOneshot {
				cs.Boot.BootSourceOverrideEnabled = server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_ONCE
			} else {
				cs.Boot.BootSourceOverrideEnabled = server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_CONTINUOUS
			}
		} else {
			cs.Boot.BootSourceOverrideEnabled = server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_DISABLED
		}
	} else if override, oerr := h.rm.GetBootOverride(ctx); oerr == nil {
		// GetBootFlags fails when no device carries a boot order; the target
		// is underivable then, but the Enabled bit is not — the in-memory
		// model may still hold an already-consumed override, or Disabled
		// while one is recorded (agent restart).
		enabled := server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_DISABLED
		if override != nil {
			enabled = server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_CONTINUOUS
			if override.Mode == bmcv1.BootOverrideModeOneshot {
				enabled = server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_ONCE
			}
		}
		cs.Boot.BootSourceOverrideEnabled = enabled
	}

	return cs, nil
}

func (h *handler) PatchComputerSystem(ctx context.Context, computerSystemPatch *server.ComputerSystemV1220ComputerSystem) error {
	boot := computerSystemPatch.Boot
	accesslog.Record(ctx, logrus.Fields{
		"action":       "patch_boot",
		"boot_target":  string(boot.BootSourceOverrideTarget),
		"boot_enabled": string(boot.BootSourceOverrideEnabled),
		"boot_mode":    string(boot.BootSourceOverrideMode),
	})

	firmwareMode, hasFirmwareMode := redfishFirmwareMode(boot.BootSourceOverrideMode)

	var bootMode resourcemanager.BootMode
	hasBootOverride := true
	switch boot.BootSourceOverrideEnabled {
	case server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_DISABLED:
		if err := h.rm.ClearBootOverrides(ctx); err != nil {
			return err
		}
		if hasFirmwareMode {
			return h.rm.SetFirmwareMode(ctx, firmwareMode)
		}
		return nil
	case server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_ONCE:
		bootMode = resourcemanager.BootModeOneshot
	case server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_CONTINUOUS:
		bootMode = resourcemanager.BootModePersistent
	case "":
		// Enabled omitted: per DSP0266, PATCH leaves absent properties
		// unchanged, so the target applies under the current override mode.
		// (ironic sends target-only PATCHes when the desired enabled state
		// already matches the reported one.)
		override, err := h.rm.GetBootOverride(ctx)
		if err != nil {
			return err
		}
		if override == nil {
			hasBootOverride = false
		} else if override.Mode == bmcv1.BootOverrideModeOneshot {
			bootMode = resourcemanager.BootModeOneshot
		} else {
			bootMode = resourcemanager.BootModePersistent
		}
	default:
		hasBootOverride = false
	}

	if !hasBootOverride {
		if hasFirmwareMode {
			return h.rm.SetFirmwareMode(ctx, firmwareMode)
		}
		return nil
	}

	var bootDevice resourcemanager.BootDevice
	switch boot.BootSourceOverrideTarget {
	case server.COMPUTERSYSTEMBOOTSOURCE_PXE:
		bootDevice = resourcemanager.BootDevicePxe
	case server.COMPUTERSYSTEMBOOTSOURCE_HDD:
		bootDevice = resourcemanager.BootDeviceHdd
	case server.COMPUTERSYSTEMBOOTSOURCE_CD:
		bootDevice = resourcemanager.BootDeviceCd
	default:
		return nil
	}

	opts := &resourcemanager.BootOptions{Mode: bootMode}
	if hasFirmwareMode {
		efiBoot := firmwareMode == resourcemanager.FirmwareModeUEFI
		opts.EFIBoot = &efiBoot
	}
	if err := h.rm.SetBootDevice(ctx, bootDevice, opts); err != nil {
		return err
	}
	return nil
}

func redfishFirmwareMode(mode server.ComputerSystemV1220BootSourceOverrideMode) (resourcemanager.FirmwareMode, bool) {
	switch mode {
	case server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEMODE_UEFI:
		return resourcemanager.FirmwareModeUEFI, true
	case server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEMODE_LEGACY:
		return resourcemanager.FirmwareModeLegacy, true
	default:
		return "", false
	}
}

func (h *handler) ComputerSystemReset(ctx context.Context, resetType server.ResourceResetType) error {
	accesslog.Record(ctx, logrus.Fields{"action": "reset", "reset_type": string(resetType)})

	powerActionMap := map[server.ResourceResetType]func(context.Context) error{
		server.RESOURCERESETTYPE_ON:                h.rm.PowerOn,
		server.RESOURCERESETTYPE_GRACEFUL_SHUTDOWN: h.rm.PowerOff,
		server.RESOURCERESETTYPE_FORCE_OFF:         h.rm.ForcePowerOff,
		server.RESOURCERESETTYPE_GRACEFUL_RESTART:  h.rm.PowerCycle,
		server.RESOURCERESETTYPE_FORCE_RESTART:     h.rm.ForcePowerCycle,
	}

	powerAction, ok := powerActionMap[resetType]
	if !ok {
		return fmt.Errorf("unsupported reset type: %s", resetType)
	}
	return powerAction(ctx)
}

// ComputerSystemSetDefaultBootOrder sets the boot order for the computer system back to default.
// TODO: Implement real default boot order setting. Right now we intentionally misuse the handler to set the first boot
// device.
func (h *handler) ComputerSystemSetDefaultBootOrder(ctx context.Context, bootDevices []string) error {
	var bootDevice resourcemanager.BootDevice
	if len(bootDevices) > 0 {
		bootDevice = resourcemanager.BootDevice(bootDevices[0])
	}
	return h.rm.SetBootDevice(ctx, bootDevice, &resourcemanager.BootOptions{Mode: resourcemanager.BootModePersistent})
}
