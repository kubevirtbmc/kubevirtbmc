package resourcemanager

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/sirupsen/logrus"
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

	namespace  string
	name       string
	systemUUID string

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
	m.systemUUID = string(vm.UID)

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

func (m *VirtualMachineResourceManager) GetSystemUUID() (string, error) {
	if m.systemUUID == "" {
		return "", fmt.Errorf("system UUID not initialized")
	}
	return m.systemUUID, nil
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

// vmDesiresRunning reports whether the run strategy (Always/RerunOnFailure/
// Manual) intends the VM to be running.
func vmDesiresRunning(vm *kubevirtv1.VirtualMachine) bool {
	rs, err := vm.RunStrategy()
	if err != nil {
		return false
	}
	switch rs {
	case kubevirtv1.RunStrategyAlways,
		kubevirtv1.RunStrategyRerunOnFailure,
		kubevirtv1.RunStrategyManual:
		return true
	default:
		return false
	}
}

// hasPendingStartRequest reports whether a Start request is queued but not
// yet consumed by virt-controller.
func hasPendingStartRequest(requests []kubevirtv1.VirtualMachineStateChangeRequest) bool {
	for _, r := range requests {
		if r.Action == kubevirtv1.StartRequest {
			return true
		}
	}
	return false
}

// hasPendingStopRequest reports whether a Stop request is queued but not
// yet consumed by virt-controller.
func hasPendingStopRequest(requests []kubevirtv1.VirtualMachineStateChangeRequest) bool {
	for _, r := range requests {
		if r.Action == kubevirtv1.StopRequest {
			return true
		}
	}
	return false
}

func (m *VirtualMachineResourceManager) PowerOn() error {
	// Try-Then-Verify: call Start first, then check VM real state on
	// failure, avoiding dependency on KubeVirt error strings.
	err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Start(m.ctx, m.name, &kubevirtv1.StartOptions{})
	if err == nil {
		return nil
	}
	vm, getErr := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if getErr != nil {
		return fmt.Errorf("start failed: %w; verify state also failed: %v", err, getErr)
	}
	// Genuinely running: Ready + run intent + no pending stop. The stop
	// check matters for Manual / RerunOnFailure, where Stop is queued via
	// StateChangeRequests and runStrategy stays set while the VMI stops.
	if vm.Status.Ready && vmDesiresRunning(vm) && !hasPendingStopRequest(vm.Status.StateChangeRequests) {
		return nil
	}
	// A queued StartRequest (KubeVirt: "stop/start already underway") means
	// the first power-on is still in flight — duplicate Start is idempotent.
	if hasPendingStartRequest(vm.Status.StateChangeRequests) &&
		!hasPendingStopRequest(vm.Status.StateChangeRequests) {
		return nil
	}
	rs, rsErr := vm.RunStrategy()
	// RunStrategyAlways: Stop flips the strategy to Halted before tearing
	// down the VMI, so a Start failure with Always means the VMI is starting.
	if rsErr == nil && rs == kubevirtv1.RunStrategyAlways {
		return nil
	}
	// Manual / RerunOnFailure: the StartRequest may already be consumed
	// while the VMI is still starting (Ready=false) — a non-final VMI means
	// power-on is underway. A final VMI is not: RerunOnFailure rejects
	// start-from-failed.
	if rsErr == nil &&
		(rs == kubevirtv1.RunStrategyManual || rs == kubevirtv1.RunStrategyRerunOnFailure) &&
		!hasPendingStopRequest(vm.Status.StateChangeRequests) {
		vmi, vmiErr := m.virtClient.KubevirtV1().VirtualMachineInstances(m.namespace).
			Get(m.ctx, m.name, metav1.GetOptions{})
		if vmiErr == nil && !vmi.IsFinal() {
			return nil
		}
	}
	// Anything else is transitional — let the protocol layer retry.
	return &ErrRetryable{Err: err}
}

func (m *VirtualMachineResourceManager) PowerOff() error {
	err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Stop(m.ctx, m.name, &kubevirtv1.StopOptions{})
	if err == nil {
		return nil
	}
	vm, getErr := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if getErr != nil {
		return fmt.Errorf("stop failed: %w; verify state also failed: %v", err, getErr)
	}
	// Genuinely off: not Ready AND no pending start. runStrategy can't be
	// trusted here — for Manual / RerunOnFailure it stays set after Stop,
	// and the start check catches the inverse Start→Stop race.
	if !vm.Status.Ready && !hasPendingStartRequest(vm.Status.StateChangeRequests) {
		return nil
	}
	return &ErrRetryable{Err: err}
}

func (m *VirtualMachineResourceManager) ForcePowerOff() error {
	err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Stop(m.ctx, m.name, &kubevirtv1.StopOptions{GracePeriod: ptr.To[int64](0)})
	if err == nil {
		return nil
	}
	vm, getErr := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if getErr != nil {
		return fmt.Errorf("force stop failed: %w; verify state also failed: %v", err, getErr)
	}
	// Same idempotency check as PowerOff.
	if !vm.Status.Ready && !hasPendingStartRequest(vm.Status.StateChangeRequests) {
		return nil
	}
	return &ErrRetryable{Err: err}
}

func (m *VirtualMachineResourceManager) PowerCycle() error {
	return m.powerCycle(false)
}

func (m *VirtualMachineResourceManager) ForcePowerCycle() error {
	return m.powerCycle(true)
}

// powerCycle restarts whenever the VM is up or a non-final VMI is present;
// only an absent/final VMI falls back to PowerOn (PowerOn with a live VMI
// is a silent no-op under RunStrategyAlways). force sets GracePeriodSeconds=0.
func (m *VirtualMachineResourceManager) powerCycle(force bool) error {
	isUp, err := m.GetPowerStatus()
	if err != nil {
		return err
	}
	opts := &kubevirtv1.RestartOptions{}
	if force {
		opts.GracePeriodSeconds = ptr.To[int64](0)
	}
	if isUp {
		return m.restartOrVerify(opts)
	}
	vmi, vmiErr := m.virtClient.KubevirtV1().VirtualMachineInstances(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if vmiErr == nil && !vmi.IsFinal() {
		logrus.Warnf("PowerCycle(force=%v): VM %s/%s not Ready but VMI present (phase=%s), issuing Restart",
			force, m.namespace, m.name, vmi.Status.Phase)
		return m.restartOrVerify(opts)
	}
	logrus.Warnf("PowerCycle(force=%v): VM %s/%s not Ready, falling back to PowerOn",
		force, m.namespace, m.name)
	return m.PowerOn()
}

// restartOrVerify issues Restart and, on failure, classifies the outcome via
// the same Try-Then-Verify pattern as PowerOn/PowerOff.
func (m *VirtualMachineResourceManager) restartOrVerify(opts *kubevirtv1.RestartOptions) error {
	err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Restart(m.ctx, m.name, opts)
	if err == nil {
		return nil
	}
	vm, getErr := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if getErr != nil {
		return fmt.Errorf("restart failed: %w; verify state also failed: %v", err, getErr)
	}
	scrs := vm.Status.StateChangeRequests
	// Full restart already queued — repeated PowerCycle is idempotent.
	if hasPendingStopRequest(scrs) && hasPendingStartRequest(scrs) {
		return nil
	}
	// Partial SCR (soft-off / start in flight) — wait and retry.
	if hasPendingStopRequest(scrs) || hasPendingStartRequest(scrs) {
		return &ErrRetryable{Err: err}
	}
	vmi, vmiErr := m.virtClient.KubevirtV1().VirtualMachineInstances(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if vmiErr == nil && !vmi.IsFinal() {
		return &ErrRetryable{Err: err}
	}
	// VMI gone between GetPowerStatus and Restart: complete the cycle via PowerOn.
	return m.PowerOn()
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
