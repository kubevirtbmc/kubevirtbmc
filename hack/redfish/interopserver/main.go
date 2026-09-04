// Command interopserver serves the kubevirtbmc Redfish API backed by the real
// VirtualMachineResourceManager running on in-memory fake clients (client-go /
// controller-runtime fakes — no apiserver, no KubeVirt), so the DMTF
// Redfish-Interop-Validator exercises production read paths in CI instead of
// a hand-maintained stub that can drift from them. Write actions land on the
// fakes and never fail: the interop validator only GETs.
//
// Swapping the fakes for envtest (real apiserver via setup-envtest) only
// changes client construction below; it buys API-semantics realism (schema
// validation, status subresource) that the validator's read-only traversal
// does not exercise.
package main

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdifake "kubevirt.io/client-go/containerizeddataimporter/fake"
	kvfake "kubevirt.io/client-go/kubevirt/fake"
	crffake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
	"kubevirt.io/kubevirtbmc/pkg/redfish"
	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

const (
	bmcUser     = "admin"
	bmcPassword = "password"
	port        = 8000
	namespace   = "interop"
	vmName      = "vm"
)

// newResourceManager wires the production resource manager to fakes preloaded
// with a running VM (one bootOrder-1 disk, no boot override) and its
// VirtualMachineBMC CR.
func newResourceManager(ctx context.Context) (*resourcemanager.VirtualMachineResourceManager, error) {
	bootOrder := uint(1)
	vm := &kubevirtv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: vmName, UID: types.UID("3f5b8c2e-7a1d-4e6b-9c0f-2a4b6d8e0f1a")},
		Spec: kubevirtv1.VirtualMachineSpec{
			Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
				Spec: kubevirtv1.VirtualMachineInstanceSpec{
					Domain: kubevirtv1.DomainSpec{
						Devices: kubevirtv1.Devices{
							Disks: []kubevirtv1.Disk{{
								Name:       "rootdisk",
								BootOrder:  &bootOrder,
								DiskDevice: kubevirtv1.DiskDevice{Disk: &kubevirtv1.DiskTarget{}},
							}},
						},
					},
				},
			},
		},
		Status: kubevirtv1.VirtualMachineStatus{Ready: true},
	}

	scheme := runtime.NewScheme()
	if err := bmcv1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	bmc := &bmcv1.VirtualMachineBMC{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: vmName},
	}

	virtClient := kvfake.NewSimpleClientset(vm)
	cdiClient := cdifake.NewSimpleClientset()
	bmcClient := crffake.NewClientBuilder().WithScheme(scheme).WithObjects(bmc).Build()

	rm := resourcemanager.NewVirtualMachineResourceManager(ctx, virtClient, cdiClient, bmcClient, vmName)
	if err := rm.Initialize(namespace, vmName); err != nil {
		return nil, err
	}
	return rm, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	rm, err := newResourceManager(ctx)
	if err != nil {
		logrus.Fatal(err)
	}

	emulator := redfish.NewEmulator(ctx, port, bmcUser, bmcPassword, rm)
	if err := emulator.Run(); err != nil {
		logrus.Fatal(err)
	}
	logrus.Infof("Redfish interop test server listening on :%d (credentials %s/%s)", port, bmcUser, bmcPassword)

	<-ctx.Done()
	emulator.Stop()
}
