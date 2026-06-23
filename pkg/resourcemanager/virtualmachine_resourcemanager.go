package resourcemanager

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/sirupsen/logrus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdiclient "kubevirt.io/client-go/containerizeddataimporter"
	kvclient "kubevirt.io/client-go/kubevirt"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/util"
)

const (
	defaultComputerSystemId = "1"
	defaultManagerId        = "BMC"
	defaultManagerName      = "Manager"
	defaultVirtualMediaId   = "CD1"
	defaultVirtualMediaName = "Virtual Media"
)

var (
	powerStateMap = map[bool]server.ResourcePowerState{
		true:  server.RESOURCEPOWERSTATE_ON,
		false: server.RESOURCEPOWERSTATE_OFF,
	}
	bootSourceMap = map[BootDevice]server.ComputerSystemBootSource{
		BootDevicePxe: server.COMPUTERSYSTEMBOOTSOURCE_PXE,
		BootDeviceHdd: server.COMPUTERSYSTEMBOOTSOURCE_HDD,
		BootDeviceCd:  server.COMPUTERSYSTEMBOOTSOURCE_CD,
	}
)

type VirtualMachineResourceManager struct {
	ctx        context.Context
	virtClient kvclient.Interface
	cdiClient  cdiclient.Interface

	namespace string
	name      string

	computerSystem ComputerSystemInterface
	manager        ManagerInterface
	virtualMedia   VirtualMediaInterface
}

func NewVirtualMachineResourceManager(
	ctx context.Context,
	virtClient kvclient.Interface,
	cdiClient cdiclient.Interface,
) *VirtualMachineResourceManager {
	return &VirtualMachineResourceManager{
		ctx:        ctx,
		virtClient: virtClient,
		cdiClient:  cdiClient,
	}
}

func (m *VirtualMachineResourceManager) Initialize(namespace, name string) error {
	vm, err := m.virtClient.KubevirtV1().VirtualMachines(namespace).Get(m.ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	m.namespace = vm.Namespace
	m.name = vm.Name

	// Initialize computer system
	m.computerSystem = NewComputerSystem(
		defaultComputerSystemId,
		strings.Join([]string{vm.Namespace, vm.Name}, "/"),
		powerStateMap[vm.Status.Ready],
	)

	// Initialize manager
	m.manager = NewManager(defaultManagerId, defaultManagerName)

	// Initialize virtual media
	m.virtualMedia = NewVirtualMedia(defaultVirtualMediaId, defaultVirtualMediaName)

	// Build relationships
	var (
		oDataComputerSystem OdataInterface = m.computerSystem
		oDataManager        OdataInterface = m.manager
	)
	if err := oDataComputerSystem.ManagedBy(oDataManager); err != nil {
		return err
	}
	if err := oDataManager.Manage(oDataComputerSystem); err != nil {
		return err
	}

	return nil
}

func (m *VirtualMachineResourceManager) GetComputerSystem() (ComputerSystemInterface, error) {
	if m.computerSystem == nil {
		return nil, fmt.Errorf("computer system not initialized")
	}

	// Update the power state just-in-time until we actually implement a control loop for it
	vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	switch vm.Status.Ready {
	case true:
		m.computerSystem.SetPowerState(server.RESOURCEPOWERSTATE_ON)
	case false:
		m.computerSystem.SetPowerState(server.RESOURCEPOWERSTATE_OFF)
	}

	return m.computerSystem, nil
}

func (m *VirtualMachineResourceManager) GetManager() (ManagerInterface, error) {
	return m.manager, nil
}

func (m *VirtualMachineResourceManager) GetVirtualMedia() (VirtualMediaInterface, error) {
	return m.virtualMedia, nil
}

func (m *VirtualMachineResourceManager) EjectMedia() error {
	if m.virtualMedia == nil {
		return fmt.Errorf("virtual media not initialized")
	}

	vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if vm.Spec.Template == nil {
		return fmt.Errorf("no template found")
	}

	cdromDisk, err := util.GetCdromDisk(vm.Spec.Template.Spec.Domain.Devices.Disks)
	if err != nil {
		return err
	}

	var dvName string
	vm.Spec.Template.Spec.Volumes = slices.DeleteFunc(vm.Spec.Template.Spec.Volumes, func(v kubevirtv1.Volume) bool {
		if v.Name == cdromDisk.Name {
			dvName = v.DataVolume.Name
			return true
		}
		return false
	})

	if dvName == "" {
		return fmt.Errorf("no media inserted")
	}

	if _, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Update(m.ctx, vm, metav1.UpdateOptions{}); err != nil {
		return err
	}

	if err := m.cdiClient.CdiV1beta1().DataVolumes(m.namespace).Delete(m.ctx, dvName, metav1.DeleteOptions{}); err != nil {
		return err
	}

	m.virtualMedia.SetVirtualMedia("", false)

	return nil
}

func (m *VirtualMachineResourceManager) InsertMedia(imageURL string) error {
	if m.virtualMedia == nil {
		return fmt.Errorf("virtual media not initialized")
	}

	vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if vm.Spec.Template == nil {
		return fmt.Errorf("no template found")
	}

	cdromDisk, err := util.GetCdromDisk(vm.Spec.Template.Spec.Domain.Devices.Disks)
	if err != nil {
		return err
	}

	imageSize, err := util.GetRemoteFileSize(imageURL)
	if err != nil {
		return err
	}

	// Create DataVolume
	dv := util.ConstructDataVolume(m.namespace, m.name, imageURL, imageSize)
	_, err = m.cdiClient.CdiV1beta1().DataVolumes(m.namespace).Create(m.ctx, dv, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	// Attach DataVolume to VirtualMachine
	volume := kubevirtv1.Volume{
		Name: cdromDisk.Name,
		VolumeSource: kubevirtv1.VolumeSource{
			DataVolume: &kubevirtv1.DataVolumeSource{
				Name:         dv.Name,
				Hotpluggable: true,
			},
		},
	}
	vm.Spec.Template.Spec.Volumes = append(vm.Spec.Template.Spec.Volumes, volume)

	if _, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Update(m.ctx, vm, metav1.UpdateOptions{}); err != nil {
		return err
	}

	m.virtualMedia.SetVirtualMedia(imageURL, true)

	return nil
}

func (m *VirtualMachineResourceManager) GetPowerStatus() (bool, error) {
	// TODO: Implement a control loop to keep the power state in sync, then we will be able to
	// return the power state from the intermediate object, i.e. ComputerSystem.
	//
	// ps := m.computerSystem.GetPowerState()
	// switch ps {
	// case server.RESOURCEPOWERSTATE_ON, server.RESOURCEPOWERSTATE_POWERING_ON:
	// 	return true, nil
	// case server.RESOURCEPOWERSTATE_OFF, server.RESOURCEPOWERSTATE_POWERING_OFF:
	// 	return false, nil
	// default:
	// 	return false, nil
	// }
	vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}

	return vm.Status.Ready, nil
}

func (m *VirtualMachineResourceManager) PowerOn() error {
	_, err := m.virtClient.KubevirtV1().VirtualMachineInstances(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to get VMI %s/%s: %w", m.namespace, m.name, err)
	}
	return m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Start(m.ctx, m.name, &kubevirtv1.StartOptions{})
}

func (m *VirtualMachineResourceManager) PowerOff() error {
	_, err := m.virtClient.KubevirtV1().VirtualMachineInstances(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get VMI %s/%s: %w", m.namespace, m.name, err)
	}
	return m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Stop(m.ctx, m.name, &kubevirtv1.StopOptions{})
}

func (m *VirtualMachineResourceManager) ForcePowerOff() error {
	_, err := m.virtClient.KubevirtV1().VirtualMachineInstances(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get VMI %s/%s: %w", m.namespace, m.name, err)
	}
	return m.virtClient.KubevirtV1().VirtualMachines(m.namespace).Stop(
		m.ctx,
		m.name,
		&kubevirtv1.StopOptions{GracePeriod: ptr.To[int64](0)},
	)
}

func (m *VirtualMachineResourceManager) PowerCycle() error {
	isUp, err := m.GetPowerStatus()
	if err != nil {
		return err
	}
	if !isUp {
		return m.PowerOn()
	}
	return m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Restart(m.ctx, m.name, &kubevirtv1.RestartOptions{})
}

func (m *VirtualMachineResourceManager) ForcePowerCycle() error {
	isUp, err := m.GetPowerStatus()
	if err != nil {
		return err
	}
	if !isUp {
		return m.PowerOn()
	}

	return m.virtClient.KubevirtV1().VirtualMachines(m.namespace).Restart(
		m.ctx,
		m.name,
		&kubevirtv1.RestartOptions{GracePeriodSeconds: ptr.To[int64](0)},
	)
}

func (m *VirtualMachineResourceManager) SetBootDevice(bootDevice BootDevice) error {
	logrus.Infof("SetBootDevice: %s", bootDevice)

	// Fetch the VM only to discover device indices for patch paths.
	// The actual mutation is done via JSON Patch to avoid full-UPDATE races.
	vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if vm.Spec.Template == nil {
		return fmt.Errorf("no template found")
	}

	disks := vm.Spec.Template.Spec.Domain.Devices.Disks
	ifaces := vm.Spec.Template.Spec.Domain.Devices.Interfaces
	if len(disks) == 0 && len(ifaces) == 0 {
		return fmt.Errorf("no bootable devices found")
	}

	// Classify disks: CDROM vs regular (Disk, LUN, Floppy)
	var cdromIndices, regularDiskIndices []int
	for i, d := range disks {
		if d.CDRom != nil {
			cdromIndices = append(cdromIndices, i)
		} else {
			regularDiskIndices = append(regularDiskIndices, i)
		}
	}

	// Collect interface indices; no classification needed.
	ifaceIndices := make([]int, len(ifaces))
	for i := range ifaceIndices {
		ifaceIndices[i] = i
	}

	// Order device groups by boot device preference
	var ordered []deviceGroup
	switch bootDevice {
	case BootDevicePxe:
		if len(ifaces) == 0 {
			return fmt.Errorf("no interfaces found for PXE boot")
		}
		ordered = []deviceGroup{
			{"interface", "/spec/template/spec/domain/devices/interfaces", ifaceIndices},
			{"disk", "/spec/template/spec/domain/devices/disks", regularDiskIndices},
			{"disk", "/spec/template/spec/domain/devices/disks", cdromIndices},
		}
	case BootDeviceHdd:
		if len(regularDiskIndices) == 0 {
			return fmt.Errorf("no regular disks found for HDD boot")
		}
		ordered = []deviceGroup{
			{"disk", "/spec/template/spec/domain/devices/disks", regularDiskIndices},
			{"interface", "/spec/template/spec/domain/devices/interfaces", ifaceIndices},
			{"disk", "/spec/template/spec/domain/devices/disks", cdromIndices},
		}
	case BootDeviceCd:
		if len(cdromIndices) == 0 {
			return fmt.Errorf("no CD-ROM found for CD boot")
		}
		ordered = []deviceGroup{
			{"disk", "/spec/template/spec/domain/devices/disks", cdromIndices},
			{"disk", "/spec/template/spec/domain/devices/disks", regularDiskIndices},
			{"interface", "/spec/template/spec/domain/devices/interfaces", ifaceIndices},
		}
	default:
		return nil
	}

	patchOps := buildBootOrderPatch(ordered)

	if len(patchOps) > 0 {
		patchData, err := json.Marshal(patchOps)
		if err != nil {
			return fmt.Errorf("failed to marshal JSON patch: %w", err)
		}

		if _, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
			Patch(m.ctx, m.name, types.JSONPatchType, patchData, metav1.PatchOptions{}); err != nil {
			logrus.WithError(err).Error("patch vm error")
			return err
		}
	}

	if m.computerSystem == nil {
		logrus.Warn("computer system not initialized")
		return nil
	}
	m.computerSystem.SetBootOverride(bootSourceMap[bootDevice])

	return nil
}

// deviceGroup represents a group of devices of the same type that share a
// common JSON Patch base path, used to order boot device priorities.
type deviceGroup struct {
	devType  string
	basePath string
	indices  []int
}

// buildBootOrderPatch creates JSON Patch operations that assign sequential
// bootOrder values (starting from 1) to every device index across the ordered
// groups. It uses "add" rather than "replace" because bootOrder may not exist
// yet on the target resource — JSON Patch "add" handles both creation and
// replacement.
func buildBootOrderPatch(ordered []deviceGroup) []map[string]any {
	patchOps := make([]map[string]any, 0)
	var order uint = 1
	for _, grp := range ordered {
		for _, idx := range grp.indices {
			op := map[string]any{
				"op":    "add",
				"path":  fmt.Sprintf("%s/%d/bootOrder", grp.basePath, idx),
				"value": order,
			}
			patchOps = append(patchOps, op)
			order++
		}
	}
	return patchOps
}
