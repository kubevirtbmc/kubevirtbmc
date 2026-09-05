package virtbmcagent

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	kvclient "kubevirt.io/client-go/kubevirt"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/rest"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
	pkgutil "kubevirt.io/kubevirtbmc/pkg/util"
	"kubevirt.io/kubevirtbmc/test/util"
)

const (
	agentNamespace      = "default"
	agentVMName         = "testvm"
	agentBMCName        = "test-bmc"
	agentSecretName     = "bmc-credentials-secret"
	issue191DiskName    = "oneshot-conflict-disk"
	issue191RemovedDisk = "oneshot-removed-disk"
	agentTestTimeout    = 60 * time.Second
	agentTestInterval   = 250 * time.Millisecond
	agentDeploymentName = "testvm-virtbmc"
	// suiteInitTimeout covers container disk image pull during suite setup.
	suiteInitTimeout     = 180 * time.Second
	vmPowerStatusTimeout = 120 * time.Second

	redfishClientPodName = util.RedfishClientPodName
	ipmitoolPodName      = util.IPMIToolPodName
)

type RedfishRequest = util.RedfishRequest
type IPMIRequest = util.IPMIRequest

type agentTestEnv struct {
	VM             *kubevirtv1.VirtualMachine
	Secret         *corev1.Secret
	BMC            *bmcv1.VirtualMachineBMC
	Namespace      string
	RedfishBaseURL string
	ServiceHost    string
	Username       string
	Password       string
}

func redfishBaseURL(vmName, namespace string) string {
	return fmt.Sprintf("http://%s/redfish/v1", serviceHostname(vmName, namespace))
}

func serviceHostname(vmName, namespace string) string {
	return fmt.Sprintf("%s-virtbmc.%s.svc.cluster.local", vmName, namespace)
}

func ensureAgentTestEnv(ctx context.Context, namespace string, k8sClient client.Client) (*agentTestEnv, error) {
	env := &agentTestEnv{
		Namespace:      namespace,
		ServiceHost:    serviceHostname(agentVMName, namespace),
		RedfishBaseURL: redfishBaseURL(agentVMName, namespace),
	}

	if err := env.ensureVMExists(ctx, k8sClient, namespace); err != nil {
		return nil, err
	}

	if err := env.ensureSecretExists(ctx, k8sClient, namespace); err != nil {
		return nil, err
	}

	if err := env.ensureBMCExists(ctx, k8sClient, namespace); err != nil {
		return nil, err
	}

	waitForAgentDeploymentReady(ctx, k8sClient, namespace, agentDeploymentName)

	return env, nil
}

func (e *agentTestEnv) ensureVMExists(ctx context.Context, k8sClient client.Client, namespace string) error {
	vm := &kubevirtv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentVMName,
			Namespace: namespace,
		},
	}
	return k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentVMName}, vm)
}

func isVMIPhase(ctx context.Context, k8sClient client.Client, namespace, vmName string, phase kubevirtv1.VirtualMachineInstancePhase) bool {
	vmi := &kubevirtv1.VirtualMachineInstance{}
	err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: vmName}, vmi)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false
		}
		return false
	}
	return vmi.Status.Phase == phase
}

func isVMIDeleted(ctx context.Context, k8sClient client.Client, namespace, vmName string) bool {
	err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: vmName}, &kubevirtv1.VirtualMachineInstance{})
	return apierrors.IsNotFound(err)
}

func waitForVMIDeleted(ctx context.Context, k8sClient client.Client, namespace string) {
	Eventually(func() bool {
		return isVMIDeleted(ctx, k8sClient, namespace, agentVMName)
	}, vmPowerStatusTimeout, agentTestInterval).Should(BeTrue(),
		"VMI %s/%s should be deleted (power off)", namespace, agentVMName)
}

func waitForVMIRunning(ctx context.Context, k8sClient client.Client, namespace string) {
	Eventually(func() bool {
		return isVMIPhase(ctx, k8sClient, namespace, agentVMName, kubevirtv1.Running)
	}, vmPowerStatusTimeout, agentTestInterval).Should(BeTrue(),
		"VMI %s/%s should reach Running phase", namespace, agentVMName)
}

// waitForVMIPresentBeforeReady waits for the soft→on startup race window:
// a non-final VMI exists while VM.Status.Ready is still false. PowerCycle
// used to fall back to PowerOn here and silently swallow reset/cycle.
func waitForVMIPresentBeforeReady(ctx context.Context, k8sClient client.Client, namespace string) {
	Eventually(func() bool {
		vm := &kubevirtv1.VirtualMachine{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentVMName}, vm); err != nil {
			return false
		}
		if vm.Status.Ready {
			return false
		}
		vmi := &kubevirtv1.VirtualMachineInstance{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentVMName}, vmi); err != nil {
			return false
		}
		return !vmi.IsFinal()
	}, vmPowerStatusTimeout, 50*time.Millisecond).Should(BeTrue(),
		"expected VMI present while VM %s/%s is not Ready (startup race window)", namespace, agentVMName)
}

func setVMRunStrategy(ctx context.Context, k8sClient client.Client, namespace string, strategy kubevirtv1.VirtualMachineRunStrategy) {
	vm := &kubevirtv1.VirtualMachine{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentVMName}, vm)).To(Succeed())
	orig := vm.DeepCopy()
	vm.Spec.RunStrategy = &strategy
	vm.Spec.Running = nil
	Expect(k8sClient.Patch(ctx, vm, client.MergeFrom(orig))).To(Succeed())
}

func waitForVMIPowerCycle(ctx context.Context, k8sClient client.Client, namespace string) {
	orig := &kubevirtv1.VirtualMachineInstance{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentVMName}, orig)).To(Succeed(),
		"VMI must exist before power cycle")
	origUID := orig.UID

	Eventually(func() bool {
		curr := &kubevirtv1.VirtualMachineInstance{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentVMName}, curr); err != nil {
			return false
		}
		if curr.Status.Phase != kubevirtv1.Running {
			return false
		}
		return curr.UID != origUID
	}, vmPowerStatusTimeout, agentTestInterval).Should(BeTrue(),
		"VMI %s/%s should be recreated with a new UID and reach Running phase", namespace, agentVMName)
}

// verifyVMBootOrder fetches the test VM and checks that bootOrder values on
// disks and interfaces match the expected maps. It retries until the timeout
// because the update goes through agent → resourcemanager → KubeVirt API.
func verifyVMBootOrder(ctx context.Context, k8sClient client.Client, namespace string, expectedDisks, expectedIfaces map[int]uint) {
	Eventually(func() bool {
		vm := &kubevirtv1.VirtualMachine{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentVMName}, vm); err != nil {
			return false
		}
		if vm.Spec.Template == nil {
			return false
		}

		disks := vm.Spec.Template.Spec.Domain.Devices.Disks
		ifaces := vm.Spec.Template.Spec.Domain.Devices.Interfaces

		for idx, expectedOrder := range expectedDisks {
			if idx >= len(disks) || disks[idx].BootOrder == nil || *disks[idx].BootOrder != expectedOrder {
				return false
			}
		}
		for idx, expectedOrder := range expectedIfaces {
			if idx >= len(ifaces) || ifaces[idx].BootOrder == nil || *ifaces[idx].BootOrder != expectedOrder {
				return false
			}
		}
		return true
	}, vmPowerStatusTimeout, agentTestInterval).Should(BeTrue(),
		"VM %s/%s boot order should match: disks=%v ifaces=%v",
		namespace, agentVMName, expectedDisks, expectedIfaces)
}

// resetBootState clears all bootOrder fields and firmware from the test VM
// and clears status.bootOverride, so each boot test starts from a clean slate.
func resetBootState(ctx context.Context, k8sClient client.Client, namespace string) {
	vm := &kubevirtv1.VirtualMachine{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentVMName}, vm); err != nil {
		return
	}
	if vm.Spec.Template == nil {
		return
	}

	var vmPatch []map[string]any
	for i := len(vm.Spec.Template.Spec.Domain.Devices.Disks) - 1; i >= 0; i-- {
		d := vm.Spec.Template.Spec.Domain.Devices.Disks[i]
		if isIssue191Disk(d.Name) {
			vmPatch = append(vmPatch, map[string]any{
				"op":   "remove",
				"path": fmt.Sprintf("/spec/template/spec/domain/devices/disks/%d", i),
			})
			continue
		}
		if d.BootOrder != nil {
			vmPatch = append(vmPatch, map[string]any{
				"op":   "remove",
				"path": fmt.Sprintf("/spec/template/spec/domain/devices/disks/%d/bootOrder", i),
			})
		}
	}
	for i, iface := range vm.Spec.Template.Spec.Domain.Devices.Interfaces {
		if iface.BootOrder != nil {
			vmPatch = append(vmPatch, map[string]any{
				"op":   "remove",
				"path": fmt.Sprintf("/spec/template/spec/domain/devices/interfaces/%d/bootOrder", i),
			})
		}
	}
	for i := len(vm.Spec.Template.Spec.Volumes) - 1; i >= 0; i-- {
		volume := vm.Spec.Template.Spec.Volumes[i]
		if isIssue191Disk(volume.Name) {
			vmPatch = append(vmPatch, map[string]any{
				"op":   "remove",
				"path": fmt.Sprintf("/spec/template/spec/volumes/%d", i),
			})
		}
	}
	// Remove firmware if it was added by an override (test VM has none).
	if vm.Spec.Template.Spec.Domain.Firmware != nil {
		vmPatch = append(vmPatch, map[string]any{
			"op":   "remove",
			"path": "/spec/template/spec/domain/firmware",
		})
	}

	if len(vmPatch) > 0 {
		patchJSON, _ := json.Marshal(vmPatch)
		_ = k8sClient.Patch(ctx, vm, client.RawPatch(types.JSONPatchType, patchJSON))
	}

	bmc := &bmcv1.VirtualMachineBMC{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentBMCName}, bmc); err != nil {
		return
	}
	if bmc.Status.BootOverride != nil {
		bmc.Status.BootOverride = nil
		_ = k8sClient.Status().Update(ctx, bmc)
	}
}

func isIssue191Disk(name string) bool {
	return name == issue191DiskName || name == issue191RemovedDisk
}

func setVMDiskBootOrder(ctx context.Context, k8sClient client.Client, namespace string, diskIndex int, order uint) {
	vm := &kubevirtv1.VirtualMachine{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentVMName}, vm)).To(Succeed())
	patch := []map[string]any{{
		"op":    "add",
		"path":  fmt.Sprintf("/spec/template/spec/domain/devices/disks/%d/bootOrder", diskIndex),
		"value": order,
	}}
	patchJSON, err := json.Marshal(patch)
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient.Patch(ctx, vm, client.RawPatch(types.JSONPatchType, patchJSON))).To(Succeed())
}

func removeVMDiskAndVolumeIfExists(ctx context.Context, k8sClient client.Client, namespace, name string) {
	vm := &kubevirtv1.VirtualMachine{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentVMName}, vm)).To(Succeed())

	var patch []map[string]any
	for i, d := range vm.Spec.Template.Spec.Domain.Devices.Disks {
		if d.Name == name {
			patch = append(patch, map[string]any{
				"op":   "remove",
				"path": fmt.Sprintf("/spec/template/spec/domain/devices/disks/%d", i),
			})
			break
		}
	}
	for i, volume := range vm.Spec.Template.Spec.Volumes {
		if volume.Name == name {
			patch = append(patch, map[string]any{
				"op":   "remove",
				"path": fmt.Sprintf("/spec/template/spec/volumes/%d", i),
			})
			break
		}
	}
	if len(patch) == 0 {
		return
	}
	patchJSON, err := json.Marshal(patch)
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient.Patch(ctx, vm, client.RawPatch(types.JSONPatchType, patchJSON))).To(Succeed())
}

func addEmptyDiskWithBootOrder(ctx context.Context, k8sClient client.Client, namespace, name string, order uint) {
	vm := &kubevirtv1.VirtualMachine{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentVMName}, vm)).To(Succeed())
	patch := []map[string]any{
		{
			"op":   "add",
			"path": "/spec/template/spec/domain/devices/disks/-",
			"value": map[string]any{
				"name":      name,
				"disk":      map[string]any{"bus": "virtio"},
				"bootOrder": order,
			},
		},
		{
			"op":   "add",
			"path": "/spec/template/spec/volumes/-",
			"value": map[string]any{
				"name":      name,
				"emptyDisk": map[string]any{"capacity": "1Gi"},
			},
		},
	}
	patchJSON, err := json.Marshal(patch)
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient.Patch(ctx, vm, client.RawPatch(types.JSONPatchType, patchJSON))).To(Succeed())
}

func verifyVMNamedBootOrders(ctx context.Context, k8sClient client.Client, namespace string, expectedDisks, expectedIfaces map[string]*uint) {
	Eventually(func() bool {
		vm := &kubevirtv1.VirtualMachine{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentVMName}, vm); err != nil {
			return false
		}
		if vm.Spec.Template == nil {
			return false
		}

		diskOrders := make(map[string]*uint)
		for _, d := range vm.Spec.Template.Spec.Domain.Devices.Disks {
			diskOrders[d.Name] = d.BootOrder
		}
		ifaceOrders := make(map[string]*uint)
		for _, iface := range vm.Spec.Template.Spec.Domain.Devices.Interfaces {
			ifaceOrders[iface.Name] = iface.BootOrder
		}
		return bootOrderMapsMatch(diskOrders, expectedDisks) && bootOrderMapsMatch(ifaceOrders, expectedIfaces)
	}, vmPowerStatusTimeout, agentTestInterval).Should(BeTrue(),
		"VM %s/%s named boot orders should match: disks=%v ifaces=%v",
		namespace, agentVMName, expectedDisks, expectedIfaces)
}

func bootOrderMapsMatch(actual, expected map[string]*uint) bool {
	if len(actual) != len(expected) {
		return false
	}
	for name, expectedOrder := range expected {
		actualOrder, ok := actual[name]
		if !ok {
			return false
		}
		if expectedOrder == nil {
			if actualOrder != nil {
				return false
			}
			continue
		}
		if actualOrder == nil || *actualOrder != *expectedOrder {
			return false
		}
	}
	return true
}

// verifyVMBootOrderNot checks that the VM's boot order does NOT match the
// given override order, i.e. a cancel has taken effect.
func verifyVMBootOrderNot(ctx context.Context, k8sClient client.Client, namespace string, expectedDisks, expectedIfaces map[int]uint) {
	Eventually(func() bool {
		vm := &kubevirtv1.VirtualMachine{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentVMName}, vm); err != nil {
			return false
		}
		if vm.Spec.Template == nil {
			return false
		}
		disks := vm.Spec.Template.Spec.Domain.Devices.Disks
		ifaces := vm.Spec.Template.Spec.Domain.Devices.Interfaces

		for idx, order := range expectedDisks {
			if idx < len(disks) && disks[idx].BootOrder != nil && *disks[idx].BootOrder == order {
				return false
			}
		}
		for idx, order := range expectedIfaces {
			if idx < len(ifaces) && ifaces[idx].BootOrder != nil && *ifaces[idx].BootOrder == order {
				return false
			}
		}
		return true
	}, vmPowerStatusTimeout, agentTestInterval).Should(BeTrue(),
		"VM %s/%s boot order should no longer match override: disks=%v ifaces=%v",
		namespace, agentVMName, expectedDisks, expectedIfaces)
}

// verifyVMFirmware checks whether the test VM's firmware is set to EFI or BIOS.
func verifyVMFirmware(ctx context.Context, k8sClient client.Client, namespace string, expectEFI bool) {
	Eventually(func() bool {
		vm := &kubevirtv1.VirtualMachine{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentVMName}, vm); err != nil {
			return false
		}
		if vm.Spec.Template == nil {
			return false
		}
		fw := vm.Spec.Template.Spec.Domain.Firmware
		if expectEFI {
			return fw != nil && fw.Bootloader != nil && fw.Bootloader.EFI != nil
		}
		// Legacy BIOS: either no firmware (KubeVirt default) or explicit BIOS.
		return fw == nil || fw.Bootloader == nil || fw.Bootloader.BIOS != nil
	}, agentTestTimeout, agentTestInterval).Should(BeTrue(),
		"VM %s/%s firmware should be EFI=%v", namespace, agentVMName, expectEFI)
}

// verifyBMCBootOverride checks whether status.bootOverride exists or not on
// the VirtualMachineBMC CR.
func verifyBMCBootOverride(ctx context.Context, k8sClient client.Client, namespace string, shouldExist bool) {
	Eventually(func() bool {
		bmc := &bmcv1.VirtualMachineBMC{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentBMCName}, bmc); err != nil {
			return !shouldExist
		}
		return (bmc.Status.BootOverride != nil) == shouldExist
	}, agentTestTimeout, agentTestInterval).Should(BeTrue(),
		"VirtualMachineBMC %s/%s should have status.bootOverride present=%v", namespace, agentBMCName, shouldExist)
}

// verifyBMCBootOverrideMode polls until status.bootOverride exists with the
// expected persistence mode. A presence-only check cannot catch a mis-parsed
// persist bit: the override is still written, just with the wrong mode.
func verifyBMCBootOverrideMode(ctx context.Context, k8sClient client.Client, namespace string, mode bmcv1.BootOverrideMode) {
	Eventually(func() bmcv1.BootOverrideMode {
		bmc := &bmcv1.VirtualMachineBMC{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentBMCName}, bmc); err != nil {
			return ""
		}
		if bmc.Status.BootOverride == nil {
			return ""
		}
		return bmc.Status.BootOverride.Mode
	}, agentTestTimeout, agentTestInterval).Should(Equal(mode),
		"VirtualMachineBMC %s/%s should have status.bootOverride.mode=%s", namespace, agentBMCName, mode)
}

func (e *agentTestEnv) ensureSecretExists(ctx context.Context, k8sClient client.Client, namespace string) error {
	secret := &corev1.Secret{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentSecretName}, secret); err != nil {
		return err
	}
	e.Secret = secret
	if u, ok := secret.Data["username"]; ok {
		e.Username = string(u)
	}
	if p, ok := secret.Data["password"]; ok {
		e.Password = string(p)
	}
	return nil
}

func (e *agentTestEnv) ensureBMCExists(ctx context.Context, k8sClient client.Client, namespace string) error {
	e.BMC = &bmcv1.VirtualMachineBMC{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentBMCName,
			Namespace: namespace,
		},
	}
	return k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentBMCName}, e.BMC)
}

func waitForAgentDeploymentReady(ctx context.Context, k8sClient client.Client, namespace, deploymentName string) {
	Eventually(func() bool {
		var deployment appsv1.Deployment
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: deploymentName}, &deployment); err != nil {
			return false
		}

		desiredReplicas := int32(1)
		if deployment.Spec.Replicas != nil {
			desiredReplicas = *deployment.Spec.Replicas
		}

		if deployment.Status.ObservedGeneration < deployment.Generation {
			return false
		}

		return deployment.Status.UpdatedReplicas >= desiredReplicas &&
			deployment.Status.ReadyReplicas >= desiredReplicas &&
			deployment.Status.AvailableReplicas >= desiredReplicas
	}, agentTestTimeout, agentTestInterval).Should(BeTrue(), "agent deployment %q should become ready", deploymentName)
}

func verifyDataVolumeExists(ctx context.Context, k8sClient client.Client, namespace, name string) {
	Eventually(func() error {
		dv := &cdiv1.DataVolume{}
		return k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, dv)
	}, agentTestTimeout, agentTestInterval).Should(Succeed(),
		"DataVolume %s/%s should exist", namespace, name)
}

func verifyDataVolumeStorageClass(ctx context.Context, k8sClient client.Client, namespace, name, want string) {
	Eventually(func() string {
		dv := &cdiv1.DataVolume{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, dv); err != nil {
			return ""
		}
		if dv.Spec.Storage == nil || dv.Spec.Storage.StorageClassName == nil {
			return ""
		}
		return *dv.Spec.Storage.StorageClassName
	}, agentTestTimeout, agentTestInterval).Should(Equal(want),
		"DataVolume %s/%s should use storageClassName %q", namespace, name, want)
}

func newStorageClass(name string) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: name},
		Provisioner: "kubevirtbmc.io/e2e-test",
	}
}

func verifyDataVolumeInsecureSkipVerify(ctx context.Context, k8sClient client.Client, namespace, name string, want bool) {
	Eventually(func() bool {
		dv := &cdiv1.DataVolume{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, dv); err != nil {
			return false
		}
		if dv.Spec.Source == nil || dv.Spec.Source.HTTP == nil || dv.Spec.Source.HTTP.InsecureSkipVerify == nil {
			return false
		}
		return *dv.Spec.Source.HTTP.InsecureSkipVerify
	}, agentTestTimeout, agentTestInterval).Should(Equal(want),
		"DataVolume %s/%s should have InsecureSkipVerify %v", namespace, name, want)
}

// testCA doubles as an unsigned "wrong" CA for negative trust tests.
type testCA struct {
	certPEM []byte
	cert    *x509.Certificate
	key     *rsa.PrivateKey
}

func generateTestCA(commonName string) testCA {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	Expect(err).NotTo(HaveOccurred())

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	Expect(err).NotTo(HaveOccurred())
	cert, err := x509.ParseCertificate(der)
	Expect(err).NotTo(HaveOccurred())

	return testCA{
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		cert:    cert,
		key:     key,
	}
}

// generateTestServerCert issues a leaf cert with the ServerAuth EKU required by Go's TLS client verification.
func generateTestServerCert(ca testCA, dnsName string) (certPEM, keyPEM []byte) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	Expect(err).NotTo(HaveOccurred())

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: dnsName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{dnsName},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	Expect(err).NotTo(HaveOccurred())

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM
}

// virtualMediaTLSServer is the in-cluster nginx Deployment/Service that serves
// a test payload over HTTPS with a self-signed cert, used to exercise
// VirtualMediaSpec's insecureSkipVerify/caBundleConfigMapRef against a real
// TLS handshake instead of a publicly-trusted URL.
const virtualMediaTLSServerName = "kubevirtbmc-e2e-vm-media-tls"

// setupVirtualMediaTLSServer also returns a ConfigMap for an unrelated CA, for negative trust tests.
func setupVirtualMediaTLSServer(ctx context.Context, k8sClient client.Client, namespace string) (imageURL, correctCAConfigMap, wrongCAConfigMap string, cleanup func()) {
	dnsName := fmt.Sprintf("%s.%s.svc.cluster.local", virtualMediaTLSServerName, namespace)

	serverCA := generateTestCA("kubevirtbmc-e2e-server-ca")
	wrongCA := generateTestCA("kubevirtbmc-e2e-wrong-ca")
	certPEM, keyPEM := generateTestServerCert(serverCA, dnsName)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: virtualMediaTLSServerName, Namespace: namespace},
		Type:       corev1.SecretTypeTLS,
		Data: map[string][]byte{
			corev1.TLSCertKey:       certPEM,
			corev1.TLSPrivateKeyKey: keyPEM,
		},
	}
	Expect(k8sClient.Create(ctx, secret)).To(Succeed())

	confCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: virtualMediaTLSServerName + "-conf", Namespace: namespace},
		Data: map[string]string{
			"default.conf": fmt.Sprintf(`server {
  listen 443 ssl;
  server_name %s;
  ssl_certificate     /etc/nginx/tls/tls.crt;
  ssl_certificate_key /etc/nginx/tls/tls.key;
  location / {
    root /usr/share/nginx/html;
  }
}
`, dnsName),
		},
	}
	Expect(k8sClient.Create(ctx, confCM)).To(Succeed())

	contentCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: virtualMediaTLSServerName + "-content", Namespace: namespace},
		Data: map[string]string{
			"test.iso": "kubevirtbmc-e2e-virtual-media-tls-test-payload",
		},
	}
	Expect(k8sClient.Create(ctx, contentCM)).To(Succeed())

	deployment := newVirtualMediaTLSDeployment(namespace)
	Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

	service := newVirtualMediaTLSService(namespace)
	Expect(k8sClient.Create(ctx, service)).To(Succeed())

	waitForAgentDeploymentReady(ctx, k8sClient, namespace, virtualMediaTLSServerName)

	correctCACM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: virtualMediaTLSServerName + "-ca-correct", Namespace: namespace},
		Data:       map[string]string{pkgutil.CABundleConfigMapKey: string(serverCA.certPEM)},
	}
	Expect(k8sClient.Create(ctx, correctCACM)).To(Succeed())

	wrongCACM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: virtualMediaTLSServerName + "-ca-wrong", Namespace: namespace},
		Data:       map[string]string{pkgutil.CABundleConfigMapKey: string(wrongCA.certPEM)},
	}
	Expect(k8sClient.Create(ctx, wrongCACM)).To(Succeed())

	cleanup = func() {
		for _, obj := range []client.Object{secret, confCM, contentCM, deployment, service, correctCACM, wrongCACM} {
			_ = k8sClient.Delete(ctx, obj)
		}
	}

	return fmt.Sprintf("https://%s/test.iso", dnsName), correctCACM.Name, wrongCACM.Name, cleanup
}

func newVirtualMediaTLSDeployment(namespace string) *appsv1.Deployment {
	replicas := int32(1)
	name := virtualMediaTLSServerName
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "nginx",
						Image: "nginx:stable",
						Ports: []corev1.ContainerPort{{ContainerPort: 443}},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "tls", MountPath: "/etc/nginx/tls", ReadOnly: true},
							{Name: "conf", MountPath: "/etc/nginx/conf.d", ReadOnly: true},
							{Name: "content", MountPath: "/usr/share/nginx/html/test.iso", SubPath: "test.iso", ReadOnly: true},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: name}}},
						{Name: "conf", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: name + "-conf"},
						}}},
						{Name: "content", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: name + "-content"},
						}}},
					},
				},
			},
		},
	}
}

func newVirtualMediaTLSService(namespace string) *corev1.Service {
	name := virtualMediaTLSServerName
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": name},
			Ports:    []corev1.ServicePort{{Port: 443, TargetPort: intstr.FromInt32(443)}},
		},
	}
}

// dataVolumeImportTimeout allows for CDI importer pod scheduling/image pull
// plus the (tiny) test payload transfer, longer than agentTestTimeout which
// covers plain API-object propagation.
const dataVolumeImportTimeout = 3 * time.Minute

func verifyDataVolumeSucceeded(ctx context.Context, k8sClient client.Client, namespace, name string) {
	Eventually(func() cdiv1.DataVolumePhase {
		dv := &cdiv1.DataVolume{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, dv); err != nil {
			return ""
		}
		return dv.Status.Phase
	}, dataVolumeImportTimeout, agentTestInterval).Should(Equal(cdiv1.Succeeded),
		"DataVolume %s/%s should reach phase Succeeded", namespace, name)
}

// InsertMedia fails before ever creating a DataVolume, so a plain Get check is enough (no race to poll for).
func verifyDataVolumeAbsent(ctx context.Context, k8sClient client.Client, namespace, name string) {
	Consistently(func() bool {
		dv := &cdiv1.DataVolume{}
		err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, dv)
		return apierrors.IsNotFound(err)
	}, 2*time.Second, 250*time.Millisecond).Should(BeTrue(),
		"DataVolume %s/%s should not have been created", namespace, name)
}

func verifyDataVolumeCertConfigMap(ctx context.Context, k8sClient client.Client, namespace, name, want string) {
	Eventually(func() string {
		dv := &cdiv1.DataVolume{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, dv); err != nil {
			return ""
		}
		if dv.Spec.Source == nil || dv.Spec.Source.HTTP == nil {
			return ""
		}
		return dv.Spec.Source.HTTP.CertConfigMap
	}, agentTestTimeout, agentTestInterval).Should(Equal(want),
		"DataVolume %s/%s should reference CertConfigMap %q", namespace, name, want)
}

func verifyDataVolumeDeleted(ctx context.Context, k8sClient client.Client, namespace, name string) {
	Eventually(func() bool {
		dv := &cdiv1.DataVolume{}
		err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, dv)
		return apierrors.IsNotFound(err)
	}, agentTestTimeout, agentTestInterval).Should(BeTrue(),
		"DataVolume %s/%s should be deleted", namespace, name)
}

func verifyVMHasDataVolumeVolume(ctx context.Context, k8sClient client.Client, namespace, vmName, dvName string) {
	Eventually(func() bool {
		vm := &kubevirtv1.VirtualMachine{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: vmName}, vm); err != nil {
			return false
		}
		if vm.Spec.Template == nil {
			return false
		}
		for _, v := range vm.Spec.Template.Spec.Volumes {
			if v.DataVolume != nil && v.DataVolume.Name == dvName {
				return true
			}
		}
		return false
	}, agentTestTimeout, agentTestInterval).Should(BeTrue(),
		"VM %s/%s should have a volume with DataVolume source %q", namespace, vmName, dvName)
}

func verifyVMHasNoDataVolumeVolume(ctx context.Context, k8sClient client.Client, namespace, vmName string) {
	Eventually(func() bool {
		vm := &kubevirtv1.VirtualMachine{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: vmName}, vm); err != nil {
			return false
		}
		if vm.Spec.Template == nil {
			return false
		}
		for _, v := range vm.Spec.Template.Spec.Volumes {
			if v.DataVolume != nil {
				return false
			}
		}
		return true
	}, agentTestTimeout, agentTestInterval).Should(BeTrue(),
		"VM %s/%s should have no DataVolume volumes", namespace, vmName)
}

// waitForGuestAgent blocks until the QEMU guest agent is connected on the
// test VMI.
func waitForGuestAgent(ctx context.Context, k8sClient client.Client, namespace string) {
	Eventually(func() bool {
		vmi := &kubevirtv1.VirtualMachineInstance{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentVMName}, vmi); err != nil {
			return false
		}
		for _, cond := range vmi.Status.Conditions {
			if cond.Type == kubevirtv1.VirtualMachineInstanceAgentConnected && cond.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	}, 10*time.Minute, 5*time.Second).Should(BeTrue(),
		"guest agent should connect for %s/%s", namespace, agentVMName)
}

// triggerGuestReboot sends a soft reboot through the QEMU guest agent.
// With rebootPolicy=Terminate on the VMI, KubeVirt destroys the VMI and
// creates a new one, consuming the oneshot override.
func triggerGuestReboot(ctx context.Context, cfg *rest.Config, k8sClient client.Client, namespace string) {
	waitForGuestAgent(ctx, k8sClient, namespace)

	virtClient, err := kvclient.NewForConfig(cfg)
	if err != nil {
		Expect(err).NotTo(HaveOccurred())
		return
	}
	err = virtClient.KubevirtV1().VirtualMachineInstances(namespace).SoftReboot(ctx, agentVMName)
	// SoftReboot reports an error because the VMI is destroyed mid-call.
	_ = err
}
