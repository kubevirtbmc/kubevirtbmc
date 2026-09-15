package virtbmc

import (
	"context"
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
	"kubevirt.io/kubevirtbmc/pkg/ipmi"
	"kubevirt.io/kubevirtbmc/pkg/redfish"
	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
	"kubevirt.io/kubevirtbmc/pkg/util"
)

type VMNameKey struct{}

type VMNamespaceKey struct{}

type Options struct {
	KubeconfigPath string
	Address        string
	IPMIPort       int
	RedfishPort    int
	BMCUser        string
	BMCPassword    string
	EnableIPMI     bool
	PodName        string
	// Standalone runs without the VirtualMachineBMC CRD: no owning CR, no
	// controller. Boot override state is kept in StateFile instead of CR
	// status; reconciliation remains identical in both modes.
	Standalone bool
	// StateFile is where boot override state is persisted in standalone mode.
	StateFile string
	// StorageClass is the agent's --storage-class flag value, used for virtual
	// media DataVolumes; empty falls back to the cluster default. In managed
	// mode the controller renders the flag from the CR's spec.storageClassName.
	StorageClass string
	// VolumeMode is the --volume-mode flag value for virtual media
	// DataVolumes; empty falls back to CDI's default (Filesystem).
	VolumeMode string
	// DataVolumeSizeMargin is the --datavolume-size-margin flag value: pad
	// inserted-media DataVolumes by this many percent; <= 0 is a no-op.
	DataVolumeSizeMargin int
	// InsecureSkipVerify is the --virtual-media-insecure-skip-verify flag
	// value: skip TLS certificate verification when fetching virtual media
	// images over https. In managed mode the controller renders it from the
	// CR's spec.redfish.virtualMedia.tls.insecureSkipVerify.
	InsecureSkipVerify bool
	// CABundleConfigMap is the --virtual-media-ca-bundle-configmap flag value:
	// name of a ConfigMap (in the VM's namespace, key "ca.pem") with the CA
	// bundle trusted when fetching virtual media images over https; rendered
	// from the CR's spec.redfish.virtualMedia.tls.caBundleConfigMapRef in
	// managed mode.
	CABundleConfigMap string
}

type VirtBMC struct {
	context     context.Context
	address     string
	ipmiPort    int
	redfishPort int
	vmNamespace string
	vmName      string

	resourceManager *resourcemanager.VirtualMachineResourceManager

	ipmiSimulator   *ipmi.Simulator
	redfishEmulator *redfish.Emulator
	enableIPMI      bool
}

func NewVirtBMC(ctx context.Context, options Options, inCluster bool) (*VirtBMC, error) {
	virtClient := NewVirtClient(options)
	cdiClient := NewCdiClient(options)

	vmNamespace := ctx.Value(VMNamespaceKey{}).(string)
	vmName := ctx.Value(VMNameKey{}).(string)

	// kubeClient serves the CR-backed StateStore in managed mode and reads CA
	// bundle ConfigMaps for virtual media https fetches in both modes.
	kubeClient := NewBMCClient(options)

	var store resourcemanager.StateStore
	if options.Standalone {
		var err error
		store, err = resourcemanager.NewFileStateStore(options.StateFile)
		if err != nil {
			return nil, err
		}
	} else {
		bmcName, err := virtualMachineBMCNameFromPodLabel(ctx, kubeClient, vmNamespace, options.PodName)
		if err != nil {
			return nil, err
		}
		store = resourcemanager.NewClusterStateStore(kubeClient, vmNamespace, bmcName)
	}
	volumeMode, err := parseVolumeMode(options.VolumeMode)
	if err != nil {
		return nil, err
	}
	resourceManager := resourcemanager.NewVirtualMachineResourceManager(
		virtClient, cdiClient, store, kubeClient,
		options.StorageClass, volumeMode, options.DataVolumeSizeMargin,
		options.InsecureSkipVerify, options.CABundleConfigMap)

	var ipmiSimulator *ipmi.Simulator
	if options.EnableIPMI {
		ipmiSimulator = ipmi.NewSimulator(options.Address, options.IPMIPort, resourceManager, options.BMCUser, options.BMCPassword)
	}

	return &VirtBMC{
		context:         ctx,
		address:         options.Address,
		ipmiPort:        options.IPMIPort,
		redfishPort:     options.RedfishPort,
		vmNamespace:     vmNamespace,
		vmName:          vmName,
		resourceManager: resourceManager,
		ipmiSimulator:   ipmiSimulator,
		redfishEmulator: redfish.NewEmulator(ctx, options.RedfishPort, options.BMCUser, options.BMCPassword, resourceManager),
		enableIPMI:      options.EnableIPMI,
	}, nil
}

// parseVolumeMode maps the --volume-mode flag value to KubeVirt's volume
// mode; empty means "CDI default".
func parseVolumeMode(s string) (*corev1.PersistentVolumeMode, error) {
	switch strings.ToLower(s) {
	case "":
		return nil, nil
	case "block":
		return util.Ptr(corev1.PersistentVolumeBlock), nil
	case "filesystem":
		return util.Ptr(corev1.PersistentVolumeFilesystem), nil
	default:
		return nil, fmt.Errorf("invalid --volume-mode %q: must be block or filesystem", s)
	}
}

func virtualMachineBMCNameFromPodLabel(ctx context.Context, bmcClient client.Client, namespace, podName string) (string, error) {
	if podName == "" {
		return "", fmt.Errorf("POD_NAME is required to resolve VirtualMachineBMC owner")
	}

	pod := &corev1.Pod{}
	if err := bmcClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: podName}, pod); err != nil {
		return "", fmt.Errorf("failed to get own pod %s/%s: %w", namespace, podName, err)
	}

	if name, ok := pod.Labels[bmcv1.VirtualMachineBMCNameLabel]; ok && name != "" {
		return name, nil
	}

	return "", fmt.Errorf("pod %s/%s has no %s label identifying its VirtualMachineBMC",
		namespace, podName, bmcv1.VirtualMachineBMCNameLabel)
}

func (b *VirtBMC) Run() error {
	logrus.Info("Initializing the the VirtBMC agent...")

	if err := b.resourceManager.Initialize(b.context, b.vmNamespace, b.vmName); err != nil {
		return fmt.Errorf("unable to initialize the resource manager: %v", err)
	}

	activeBootOverride, err := b.resourceManager.ReconcileBootOverride(b.context)
	if err != nil {
		return fmt.Errorf("unable to reconcile boot override: %v", err)
	}

	// Start the IPMI simulator
	if b.ipmiSimulator != nil {
		if err := b.ipmiSimulator.Run(); err != nil {
			return fmt.Errorf("unable to run the ipmi simulator: %v", err)
		}
		logrus.Infof("IPMI service listens on %s:%d", b.address, b.ipmiPort)
	}

	// Start the Redfish emulator
	if err := b.redfishEmulator.Run(); err != nil {
		return fmt.Errorf("unable to run the redfish emulator: %v", err)
	}
	logrus.Infof("Redfish service listens on %s:%d", b.address, b.redfishPort)

	go b.runBootOverrideReconcile(activeBootOverride)

	<-b.context.Done()
	logrus.Info("Gracefully shutting down the VirtBMC agent...")
	if b.ipmiSimulator != nil {
		b.ipmiSimulator.Stop()
	}
	b.redfishEmulator.Stop()

	return nil
}
