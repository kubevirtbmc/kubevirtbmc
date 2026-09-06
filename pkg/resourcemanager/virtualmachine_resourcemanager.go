package resourcemanager

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdiclient "kubevirt.io/client-go/containerizeddataimporter"
	kvclient "kubevirt.io/client-go/kubevirt"
	"kubevirt.io/kubevirtbmc/pkg/accesslog"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
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
	virtClient kvclient.Interface
	cdiClient  cdiclient.Interface
	bmcClient  client.Client
	bmcName    string

	namespace  string
	name       string
	systemUUID string

	computerSystem ComputerSystemInterface
	manager        ManagerInterface
	virtualMedia   VirtualMediaInterface
}

func NewVirtualMachineResourceManager(
	virtClient kvclient.Interface,
	cdiClient cdiclient.Interface,
	bmcClient client.Client,
	bmcName string,
) *VirtualMachineResourceManager {
	return &VirtualMachineResourceManager{
		virtClient: virtClient,
		cdiClient:  cdiClient,
		bmcClient:  bmcClient,
		bmcName:    bmcName,
	}
}

func (m *VirtualMachineResourceManager) Initialize(ctx context.Context, namespace, name string) error {
	vm, err := m.virtClient.KubevirtV1().VirtualMachines(namespace).Get(ctx, name, metav1.GetOptions{})
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

func (m *VirtualMachineResourceManager) GetComputerSystem(ctx context.Context) (ComputerSystemInterface, error) {
	if m.computerSystem == nil {
		return nil, fmt.Errorf("computer system not initialized")
	}

	// Update the power state just-in-time until we actually implement a control loop for it
	vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(ctx, m.name, metav1.GetOptions{})
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

func (m *VirtualMachineResourceManager) GetManager(ctx context.Context) (ManagerInterface, error) {
	return m.manager, nil
}

func (m *VirtualMachineResourceManager) GetVirtualMedia(ctx context.Context) (VirtualMediaInterface, error) {
	return m.virtualMedia, nil
}

func (m *VirtualMachineResourceManager) GetSystemUUID(ctx context.Context) (string, error) {
	if m.systemUUID == "" {
		return "", fmt.Errorf("system UUID not initialized")
	}
	return m.systemUUID, nil
}

func (m *VirtualMachineResourceManager) EjectMedia(ctx context.Context) error {
	if m.virtualMedia == nil {
		return fmt.Errorf("virtual media not initialized")
	}

	vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(ctx, m.name, metav1.GetOptions{})
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
		Update(ctx, vm, metav1.UpdateOptions{}); err != nil {
		return err
	}

	if err := m.cdiClient.CdiV1beta1().DataVolumes(m.namespace).Delete(ctx, dvName, metav1.DeleteOptions{}); err != nil {
		return err
	}

	m.virtualMedia.SetVirtualMedia("", false)

	return nil
}

func (m *VirtualMachineResourceManager) InsertMedia(ctx context.Context, imageURL string) error {
	if m.virtualMedia == nil {
		return fmt.Errorf("virtual media not initialized")
	}

	vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(ctx, m.name, metav1.GetOptions{})
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

	// A missing BMC client/object means no StorageClassName/VirtualMedia override is configured, not a failure.
	var (
		storageClassName   string
		insecureSkipVerify bool
		caBundleConfigMap  string
	)

	if m.bmcClient != nil {
		var bmc bmcv1.VirtualMachineBMC
		if err := m.bmcClient.Get(ctx, types.NamespacedName{Namespace: m.namespace, Name: m.bmcName}, &bmc); err != nil {
			if !apierrors.IsNotFound(err) {
				return err
			}
		} else {
			if bmc.Spec.StorageClassName != nil {
				storageClassName = *bmc.Spec.StorageClassName
			}
			if bmc.Spec.Redfish != nil && bmc.Spec.Redfish.VirtualMedia != nil {
				if bmc.Spec.Redfish.VirtualMedia.InsecureSkipVerify != nil {
					insecureSkipVerify = *bmc.Spec.Redfish.VirtualMedia.InsecureSkipVerify
				}
				if bmc.Spec.Redfish.VirtualMedia.CABundleConfigMapRef != nil {
					caBundleConfigMap = bmc.Spec.Redfish.VirtualMedia.CABundleConfigMapRef.Name
				}
			}
		}
	}

	var caBundle []byte
	if caBundleConfigMap != "" {
		var cm corev1.ConfigMap
		if err := m.bmcClient.Get(ctx, types.NamespacedName{Namespace: m.namespace, Name: caBundleConfigMap}, &cm); err != nil {
			return fmt.Errorf("failed to get CA bundle ConfigMap %q: %w", caBundleConfigMap, err)
		}
		caBundle = []byte(cm.Data[util.CABundleConfigMapKey])
	}

	imageSize, err := util.GetRemoteFileSize(imageURL, insecureSkipVerify, caBundle)
	if err != nil {
		return err
	}

	// Create DataVolume
	dv := util.ConstructDataVolume(util.DataVolumeOptions{
		Namespace:          m.namespace,
		Name:               m.name,
		URL:                imageURL,
		Size:               imageSize,
		StorageClassName:   storageClassName,
		InsecureSkipVerify: insecureSkipVerify,
		CertConfigMap:      caBundleConfigMap,
	})
	_, err = m.cdiClient.CdiV1beta1().DataVolumes(m.namespace).Create(ctx, dv, metav1.CreateOptions{})
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
		Update(ctx, vm, metav1.UpdateOptions{}); err != nil {
		return err
	}

	m.virtualMedia.SetVirtualMedia(imageURL, true)

	return nil
}

func (m *VirtualMachineResourceManager) GetPowerStatus(ctx context.Context) (bool, error) {
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
		Get(ctx, m.name, metav1.GetOptions{})
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

func (m *VirtualMachineResourceManager) PowerOn(ctx context.Context) error {
	// Try-Then-Verify: call Start first, then check VM real state on
	// failure, avoiding dependency on KubeVirt error strings.
	err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Start(ctx, m.name, &kubevirtv1.StartOptions{})
	if err == nil {
		return nil
	}
	vm, getErr := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(ctx, m.name, metav1.GetOptions{})
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
			Get(ctx, m.name, metav1.GetOptions{})
		if vmiErr == nil && !vmi.IsFinal() {
			return nil
		}
	}
	// Anything else is transitional — let the protocol layer retry.
	return &ErrRetryable{Err: err}
}

func (m *VirtualMachineResourceManager) PowerOff(ctx context.Context) error {
	err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Stop(ctx, m.name, &kubevirtv1.StopOptions{})
	if err == nil {
		return nil
	}
	vm, getErr := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(ctx, m.name, metav1.GetOptions{})
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

func (m *VirtualMachineResourceManager) ForcePowerOff(ctx context.Context) error {
	err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Stop(ctx, m.name, &kubevirtv1.StopOptions{GracePeriod: ptr.To[int64](0)})
	if err == nil {
		return nil
	}
	vm, getErr := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(ctx, m.name, metav1.GetOptions{})
	if getErr != nil {
		return fmt.Errorf("force stop failed: %w; verify state also failed: %v", err, getErr)
	}
	// Same idempotency check as PowerOff.
	if !vm.Status.Ready && !hasPendingStartRequest(vm.Status.StateChangeRequests) {
		return nil
	}
	return &ErrRetryable{Err: err}
}

func (m *VirtualMachineResourceManager) PowerCycle(ctx context.Context) error {
	return m.powerCycle(ctx, false)
}

func (m *VirtualMachineResourceManager) ForcePowerCycle(ctx context.Context) error {
	return m.powerCycle(ctx, true)
}

// powerCycle restarts whenever the VM is up or a non-final VMI is present;
// only an absent/final VMI falls back to PowerOn (PowerOn with a live VMI
// is a silent no-op under RunStrategyAlways). force sets GracePeriodSeconds=0.
//
// The path taken is recorded onto the access-log line rather than logged
// outright: the caller asked for a reset, so which KubeVirt verb served it only
// matters alongside the outcome of that request.
func (m *VirtualMachineResourceManager) powerCycle(ctx context.Context, force bool) error {
	isUp, err := m.GetPowerStatus(ctx)
	if err != nil {
		return err
	}
	opts := &kubevirtv1.RestartOptions{}
	if force {
		opts.GracePeriodSeconds = ptr.To[int64](0)
	}
	if isUp {
		return m.restartOrVerify(ctx, opts)
	}
	vmi, vmiErr := m.virtClient.KubevirtV1().VirtualMachineInstances(m.namespace).
		Get(ctx, m.name, metav1.GetOptions{})
	if vmiErr == nil && !vmi.IsFinal() {
		accesslog.Record(ctx, logrus.Fields{
			"power_cycle": "restart",
			"vm_ready":    false,
			"vmi_phase":   vmi.Status.Phase,
		})
		return m.restartOrVerify(ctx, opts)
	}
	accesslog.Record(ctx, logrus.Fields{"power_cycle": "power_on", "vm_ready": false})
	return m.PowerOn(ctx)
}

// restartOrVerify issues Restart and, on failure, classifies the outcome via
// the same Try-Then-Verify pattern as PowerOn/PowerOff.
func (m *VirtualMachineResourceManager) restartOrVerify(ctx context.Context, opts *kubevirtv1.RestartOptions) error {
	err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Restart(ctx, m.name, opts)
	if err == nil {
		return nil
	}
	vm, getErr := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(ctx, m.name, metav1.GetOptions{})
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
		Get(ctx, m.name, metav1.GetOptions{})
	if vmiErr == nil && !vmi.IsFinal() {
		return &ErrRetryable{Err: err}
	}
	// VMI gone between GetPowerStatus and Restart: complete the cycle via PowerOn.
	accesslog.Record(ctx, logrus.Fields{"power_cycle": "power_on"})
	return m.PowerOn(ctx)
}

// GetBootFlags derives the current boot flags — boot device (lowest bootOrder),
// firmware type, and persistence mode — from the VM template spec and
// status.bootOverride on the VirtualMachineBMC CR.
func (m *VirtualMachineResourceManager) GetBootFlags(ctx context.Context) (*BootFlagsState, error) {
	disks, ifaces := m.getBootDevices(ctx)
	if len(disks) == 0 && len(ifaces) == 0 {
		return nil, fmt.Errorf("no bootable devices found")
	}

	bootDev, ok := findFirstBootDevice(disks, ifaces)
	if !ok {
		return nil, fmt.Errorf("no boot order set on any device")
	}

	overrideActive := false
	mode := BootModePersistent
	override, err := m.GetBootOverride(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check boot override status: %w", err)
	}
	if override != nil {
		overrideActive = true
		if override.Mode == bmcv1.BootOverrideModeOneshot {
			mode = BootModeOneshot
		}
	}

	efi := m.isEFIBoot(ctx)

	return &BootFlagsState{
		BootDevice:     bootDev,
		Mode:           mode,
		EFIBoot:        efi,
		OverrideActive: overrideActive,
	}, nil
}

// getBootDevices fetches the disk and interface lists from the VM template spec.
// The VM spec is the authoritative source for "what will boot next": KubeVirt
// does not live-update a running VMI when the VM template changes (the VM is
// marked RestartRequired instead), so the VMI may be stale after SetBootDevice.
func (m *VirtualMachineResourceManager) getBootDevices(ctx context.Context) ([]kubevirtv1.Disk, []kubevirtv1.Interface) {
	vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(ctx, m.name, metav1.GetOptions{})
	if err != nil {
		accesslog.Logger(ctx).WithError(err).Warn("failed to get VM for boot flags readback")
		return nil, nil
	}
	if vm.Spec.Template == nil {
		return nil, nil
	}
	return vm.Spec.Template.Spec.Domain.Devices.Disks,
		vm.Spec.Template.Spec.Domain.Devices.Interfaces
}

// findFirstBootDevice finds the device with the lowest bootOrder value and
// returns its boot device type (Pxe for interfaces, Hdd for regular disks,
// Cd for CDROM disks). Returns "", false when no bootOrder is set.
func findFirstBootDevice(disks []kubevirtv1.Disk, ifaces []kubevirtv1.Interface) (BootDevice, bool) {
	type candidate struct {
		bootOrder uint
		device    BootDevice
	}
	var first *candidate

	check := func(order *uint, device BootDevice) {
		if order == nil {
			return
		}
		if first == nil || *order < first.bootOrder {
			first = &candidate{bootOrder: *order, device: device}
		}
	}

	for i := range disks {
		isCDRom := disks[i].CDRom != nil
		dev := BootDeviceHdd
		if isCDRom {
			dev = BootDeviceCd
		}
		check(disks[i].BootOrder, dev)
	}
	for i := range ifaces {
		check(ifaces[i].BootOrder, BootDevicePxe)
	}

	if first == nil {
		return "", false
	}
	return first.device, true
}

// isEFIBoot returns true if the VM firmware bootloader is set to EFI.
func (m *VirtualMachineResourceManager) isEFIBoot(ctx context.Context) bool {
	vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(ctx, m.name, metav1.GetOptions{})
	if err != nil {
		return false
	}
	if vm.Spec.Template == nil {
		return false
	}
	return !currentFirmwareIsBios(vm)
}

func (m *VirtualMachineResourceManager) SetBootDevice(ctx context.Context, bootDevice BootDevice, opts *BootOptions) error {
	// Default to persistent when no options provided.
	if opts == nil {
		opts = &BootOptions{Mode: BootModePersistent}
	}

	// Fetch the VM only to discover device indices for patch paths.
	// The actual mutation is done via JSON Patch to avoid full-UPDATE races.
	vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(ctx, m.name, metav1.GetOptions{})
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

	// KubeVirt accepts template changes on running VMs and marks them
	// RestartRequired when needed.
	if opts.EFIBoot != nil {
		patchOps = append(patchOps, BuildFirmwarePatch(vm, *opts.EFIBoot)...)
	}

	if len(patchOps) > 0 {
		patchData, err := json.Marshal(patchOps)
		if err != nil {
			return fmt.Errorf("failed to marshal JSON patch: %w", err)
		}

		if _, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
			Patch(ctx, m.name, types.JSONPatchType, patchData, metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("failed to patch VM boot devices: %w", err)
		}
	}

	if err := m.handleBootOrderBackup(ctx, vm, disks, ifaces, opts); err != nil {
		return err
	}

	m.updateComputerSystemBootState(ctx, bootDevice, opts)
	return nil
}

// handleBootOrderBackup saves a oneshot backup or a persistent override marker
// to status.bootOverride based on the boot mode. Call after the VM patch succeeds.
func (m *VirtualMachineResourceManager) handleBootOrderBackup(
	ctx context.Context,
	vm *kubevirtv1.VirtualMachine,
	disks []kubevirtv1.Disk,
	ifaces []kubevirtv1.Interface,
	opts *BootOptions,
) error {
	switch opts.Mode {
	case BootModeOneshot:
		// Preserve an existing oneshot backup (issued before the VM
		// rebooted): the boot order captured on the first oneshot is the
		// state to restore. A persistent marker carries no boot order
		// data, so overwrite it with a fresh backup.
		existing, err := m.GetBootOverride(ctx)
		if err != nil {
			return fmt.Errorf("failed to check for existing boot override: %w", err)
		}
		if existing != nil && existing.Mode != bmcv1.BootOverrideModePersistent {
			return nil
		}

		override := &bmcv1.BootOverrideStatus{
			Mode:       bmcv1.BootOverrideModeOneshot,
			BootOrders: make(map[string]uint),
		}

		// A VMI UID change after this backup means the oneshot was consumed.
		if vmi, err := m.virtClient.KubevirtV1().VirtualMachineInstances(m.namespace).
			Get(ctx, m.name, metav1.GetOptions{}); err == nil {
			override.VMIUID = string(vmi.UID)
		}

		// Recorded unconditionally: a later oneshot (before reboot) may be
		// the one that changes firmware, and the original value must
		// already be in the backup by then. Unset firmware is KubeVirt's
		// default (Legacy).
		override.OriginalFirmware = currentFirmwareType(vm)

		// Zero value = device existed without a bootOrder, distinguishing
		// it from devices added after this backup was saved.
		for _, d := range disks {
			override.BootOrders[diskBackupKey(d)] = bootOrderValue(d.BootOrder)
		}
		for _, iface := range ifaces {
			override.BootOrders[interfaceBackupKey(iface)] = bootOrderValue(iface.BootOrder)
		}

		if err := m.saveBootOverride(ctx, override); err != nil {
			return fmt.Errorf("failed to save boot override: %w", err)
		}
	case BootModePersistent:
		if err := m.saveBootOverride(ctx, &bmcv1.BootOverrideStatus{
			Mode: bmcv1.BootOverrideModePersistent,
		}); err != nil {
			return fmt.Errorf("failed to save persistent override marker: %w", err)
		}
	}
	return nil
}

// updateComputerSystemBootState updates the Redfish ComputerSystem model
// with the boot override mode and optional firmware mode.
func (m *VirtualMachineResourceManager) updateComputerSystemBootState(ctx context.Context, bootDevice BootDevice, opts *BootOptions) {
	if m.computerSystem == nil {
		accesslog.Logger(ctx).Warn("computer system not initialized")
		return
	}

	overrideMode := OverrideModeContinuous
	if opts.Mode == BootModeOneshot {
		overrideMode = OverrideModeOnce
	}
	m.computerSystem.SetBootOverride(bootDevice, overrideMode)

	if opts.EFIBoot != nil {
		if *opts.EFIBoot {
			m.computerSystem.SetFirmwareMode(FirmwareModeUEFI)
		} else {
			m.computerSystem.SetFirmwareMode(FirmwareModeLegacy)
		}
	}
}

func (m *VirtualMachineResourceManager) SetFirmwareMode(ctx context.Context, mode FirmwareMode) error {
	var efi bool
	switch mode {
	case FirmwareModeUEFI:
		efi = true
	case FirmwareModeLegacy:
		efi = false
	default:
		return fmt.Errorf("unsupported firmware mode: %s", mode)
	}

	vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(ctx, m.name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if vm.Spec.Template == nil {
		return fmt.Errorf("no template found")
	}

	patchOps := BuildFirmwarePatch(vm, efi)
	if len(patchOps) > 0 {
		patchData, err := json.Marshal(patchOps)
		if err != nil {
			return fmt.Errorf("failed to marshal JSON patch: %w", err)
		}

		if _, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
			Patch(ctx, m.name, types.JSONPatchType, patchData, metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("failed to patch VM firmware mode: %w", err)
		}
	}

	if m.computerSystem == nil {
		accesslog.Logger(ctx).Warn("computer system not initialized")
		return nil
	}
	m.computerSystem.SetFirmwareMode(mode)
	return nil
}

// ClearBootOverrides cancels the current boot override. If a oneshot backup
// exists (the override hasn't been consumed yet), it restores the original boot
// order from the backup. It always clears status.bootOverride and resets the
// ComputerSystem boot override state to Disabled.
//
// Note: if the override was persistent (no backup), device bootOrders are left
// as-is since there is no saved "original" state to restore to. The
// ComputerSystem override is still marked Disabled.
func (m *VirtualMachineResourceManager) ClearBootOverrides(ctx context.Context) error {
	vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(ctx, m.name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if vm.Spec.Template == nil {
		return fmt.Errorf("no template found")
	}

	// If a oneshot backup exists, restore the original boot order from it.
	override, err := m.GetBootOverride(ctx)
	if err != nil {
		return fmt.Errorf("failed to read boot override status: %w", err)
	}

	var patchOps []map[string]any
	if override != nil {
		patchOps = BuildBootOrderRestorePatch(vm, override)
	}

	if len(patchOps) > 0 {
		patchData, err := json.Marshal(patchOps)
		if err != nil {
			return fmt.Errorf("failed to marshal JSON patch: %w", err)
		}

		if _, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
			Patch(ctx, m.name, types.JSONPatchType, patchData, metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("failed to restore VM boot order: %w", err)
		}
	}

	if err := m.clearBootOverride(ctx); err != nil {
		accesslog.Logger(ctx).WithError(err).Warn("failed to clear boot override status")
	}

	if m.computerSystem != nil {
		m.computerSystem.ClearBootOverride()
	}

	return nil
}

// saveBootOverride writes the boot override to status.bootOverride on the
// VirtualMachineBMC CR. Read-modify-write with conflict retry REPLACES the
// whole bootOverride value — a merge patch would linger stale keys from a
// previous override (e.g. bootOrders surviving a oneshot→persistent overwrite).
func (m *VirtualMachineResourceManager) saveBootOverride(ctx context.Context, override *bmcv1.BootOverrideStatus) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		bmc := &bmcv1.VirtualMachineBMC{}
		if err := m.bmcClient.Get(ctx, client.ObjectKey{Namespace: m.namespace, Name: m.bmcName}, bmc); err != nil {
			return fmt.Errorf("failed to get VirtualMachineBMC: %w", err)
		}
		bmc.Status.BootOverride = override
		if err := m.bmcClient.Status().Update(ctx, bmc); err != nil {
			return fmt.Errorf("failed to update VirtualMachineBMC status: %w", err)
		}
		return nil
	})
}

// clearBootOverride removes status.bootOverride from the VirtualMachineBMC CR.
func (m *VirtualMachineResourceManager) clearBootOverride(ctx context.Context) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		bmc := &bmcv1.VirtualMachineBMC{}
		if err := m.bmcClient.Get(ctx, client.ObjectKey{Namespace: m.namespace, Name: m.bmcName}, bmc); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("failed to get VirtualMachineBMC: %w", err)
		}
		if bmc.Status.BootOverride == nil {
			return nil
		}
		bmc.Status.BootOverride = nil
		if err := m.bmcClient.Status().Update(ctx, bmc); err != nil {
			return fmt.Errorf("failed to update VirtualMachineBMC status: %w", err)
		}
		return nil
	})
}

// currentFirmwareType returns the VM firmware bootloader type (Legacy when
// firmware is unset, KubeVirt's default).
func currentFirmwareType(vm *kubevirtv1.VirtualMachine) bmcv1.FirmwareType {
	if currentFirmwareIsBios(vm) {
		return bmcv1.FirmwareTypeLegacy
	}
	return bmcv1.FirmwareTypeUEFI
}

// diskBackupKey / interfaceBackupKey build the BootOrders map keys. The class
// prefix follows the BMC device vocabulary (disk/cdrom/interface) rather than
// the KubeVirt list layout (where CDROMs live in the disks list); it also
// disambiguates same-named devices across the two lists.
func diskBackupKey(d kubevirtv1.Disk) string {
	if d.CDRom != nil {
		return "cdrom:" + d.Name
	}
	return "disk:" + d.Name
}

func interfaceBackupKey(iface kubevirtv1.Interface) string {
	return "interface:" + iface.Name
}

// currentFirmwareIsBios returns true if the VM currently uses Legacy BIOS,
// including KubeVirt's default BIOS when firmware is unset.
func currentFirmwareIsBios(vm *kubevirtv1.VirtualMachine) bool {
	if vm.Spec.Template == nil || vm.Spec.Template.Spec.Domain.Firmware == nil ||
		vm.Spec.Template.Spec.Domain.Firmware.Bootloader == nil {
		return true // KubeVirt default is BIOS
	}
	return vm.Spec.Template.Spec.Domain.Firmware.Bootloader.EFI == nil
}

// buildFirmwarePatch creates JSON Patch operations to switch the VM firmware
// bootloader between BIOS and EFI.
func BuildFirmwarePatch(vm *kubevirtv1.VirtualMachine, efi bool) []map[string]any {
	fwPath := "/spec/template/spec/domain/firmware"
	blPath := fwPath + "/bootloader"
	efiPath := blPath + "/efi"
	biosPath := blPath + "/bios"

	if vm.Spec.Template.Spec.Domain.Firmware == nil {
		return []map[string]any{
			{
				"op":   "add",
				"path": fwPath,
				"value": map[string]any{
					"bootloader": bootloaderValue(vm, efi),
				},
			},
		}
	}

	if vm.Spec.Template.Spec.Domain.Firmware.Bootloader == nil {
		return []map[string]any{
			{
				"op":    "add",
				"path":  blPath,
				"value": bootloaderValue(vm, efi),
			},
		}
	}

	bootloader := vm.Spec.Template.Spec.Domain.Firmware.Bootloader
	var ops []map[string]any
	if efi {
		if bootloader.EFI == nil {
			ops = append(ops, map[string]any{
				"op":    "add",
				"path":  efiPath,
				"value": efiValue(vm),
			})
		}
		if bootloader.BIOS != nil {
			ops = append(ops, map[string]any{
				"op":   "remove",
				"path": biosPath,
			})
		}
	} else {
		if bootloader.BIOS == nil {
			ops = append(ops, map[string]any{
				"op":    "add",
				"path":  biosPath,
				"value": map[string]any{},
			})
		}
		if bootloader.EFI != nil {
			ops = append(ops, map[string]any{
				"op":   "remove",
				"path": efiPath,
			})
		}
	}
	return ops
}

func bootloaderValue(vm *kubevirtv1.VirtualMachine, efi bool) map[string]any {
	if efi {
		return map[string]any{"efi": efiValue(vm)}
	}
	return map[string]any{"bios": map[string]any{}}
}

func efiValue(vm *kubevirtv1.VirtualMachine) map[string]any {
	return map[string]any{"secureBoot": smmEnabled(vm)}
}

func smmEnabled(vm *kubevirtv1.VirtualMachine) bool {
	if vm.Spec.Template == nil || vm.Spec.Template.Spec.Domain.Features == nil {
		return false
	}
	smm := vm.Spec.Template.Spec.Domain.Features.SMM
	return smm != nil && (smm.Enabled == nil || *smm.Enabled)
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

// BuildBootOrderRestorePatch creates JSON Patch operations that restore boot
// order and firmware state from a BootOverrideStatus backup. Devices added
// after the backup are left untouched; if they occupy a saved bootOrder, the
// old device's conflicting bootOrder is cleared instead.
func BuildBootOrderRestorePatch(vm *kubevirtv1.VirtualMachine, backup *bmcv1.BootOverrideStatus) []map[string]any {
	var patchOps []map[string]any
	addedBootOrders := collectAddedDeviceBootOrders(vm, backup)

	for i, d := range vm.Spec.Template.Spec.Domain.Devices.Disks {
		key := diskBackupKey(d)
		savedOrder, savedDevice := savedBootOrder(backup, key)
		patchOps = append(patchOps, buildBootOrderRestoreOps(
			fmt.Sprintf("/spec/template/spec/domain/devices/disks/%d/bootOrder", i),
			d.BootOrder,
			savedOrder,
			savedDevice,
			addedBootOrders,
		)...)
	}

	for i, iface := range vm.Spec.Template.Spec.Domain.Devices.Interfaces {
		key := interfaceBackupKey(iface)
		savedOrder, savedDevice := savedBootOrder(backup, key)
		patchOps = append(patchOps, buildBootOrderRestoreOps(
			fmt.Sprintf("/spec/template/spec/domain/devices/interfaces/%d/bootOrder", i),
			iface.BootOrder,
			savedOrder,
			savedDevice,
			addedBootOrders,
		)...)
	}

	// Skip when firmware already matches the backup — otherwise the patch
	// would materialize an explicit firmware.bios section on VMs that had
	// firmware unset (KubeVirt's default).
	if backup.OriginalFirmware != "" && currentFirmwareType(vm) != backup.OriginalFirmware {
		patchOps = append(patchOps, BuildFirmwarePatch(vm, backup.OriginalFirmware == bmcv1.FirmwareTypeUEFI)...)
	}

	return patchOps
}

// bootOrderValue flattens a *uint bootOrder for storage in
// BootOverrideStatus.BootOrders: 0 means "device existed without a bootOrder"
// (bootOrder counts from 1, so 0 is an unambiguous sentinel).
func bootOrderValue(order *uint) uint {
	if order == nil {
		return 0
	}
	return *order
}

// savedBootOrder returns the bootOrder recorded for key in the backup, and
// whether the device existed at backup time. (nil, true) means "existed
// without a bootOrder" (stored as the 0 sentinel).
func savedBootOrder(backup *bmcv1.BootOverrideStatus, key string) (*uint, bool) {
	v, ok := backup.BootOrders[key]
	if !ok || v == 0 {
		return nil, ok
	}
	return util.Ptr(v), true
}

func collectAddedDeviceBootOrders(vm *kubevirtv1.VirtualMachine, backup *bmcv1.BootOverrideStatus) map[uint]bool {
	orders := make(map[uint]bool)
	for _, d := range vm.Spec.Template.Spec.Domain.Devices.Disks {
		if !hasSavedDevice(backup, diskBackupKey(d)) && d.BootOrder != nil {
			orders[*d.BootOrder] = true
		}
	}
	for _, iface := range vm.Spec.Template.Spec.Domain.Devices.Interfaces {
		if !hasSavedDevice(backup, interfaceBackupKey(iface)) && iface.BootOrder != nil {
			orders[*iface.BootOrder] = true
		}
	}
	return orders
}

func hasSavedDevice(backup *bmcv1.BootOverrideStatus, key string) bool {
	_, ok := backup.BootOrders[key]
	return ok
}

func buildBootOrderRestoreOps(path string, currentOrder, savedOrder *uint, savedDevice bool, addedBootOrders map[uint]bool) []map[string]any {
	if !savedDevice {
		return nil
	}
	if savedOrder == nil {
		if currentOrder == nil {
			return nil
		}
		return []map[string]any{{"op": "remove", "path": path}}
	}
	if addedBootOrders[*savedOrder] {
		if currentOrder == nil {
			return nil
		}
		return []map[string]any{{"op": "remove", "path": path}}
	}
	return []map[string]any{{
		"op":    "add",
		"path":  path,
		"value": *savedOrder,
	}}
}

// GetBootOverride reads status.bootOverride from the VirtualMachineBMC CR.
// Returns nil (without error) when no override is recorded.
func (m *VirtualMachineResourceManager) GetBootOverride(ctx context.Context) (*bmcv1.BootOverrideStatus, error) {
	bmc := &bmcv1.VirtualMachineBMC{}
	if err := m.bmcClient.Get(ctx, client.ObjectKey{Namespace: m.namespace, Name: m.bmcName}, bmc); err != nil {
		return nil, err
	}
	return bmc.Status.BootOverride, nil
}
