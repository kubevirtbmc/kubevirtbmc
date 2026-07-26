/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package bootorderrestore

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	kubevirtv1 "kubevirt.io/api/core/v1"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
)

var _ = Describe("BootOrderRestore Controller", func() {
	const (
		namespace = "default"
		vmName    = "test-boot-vm"
		bmcName   = "test-boot-bmc"
		timeout   = 10 * time.Second
		interval  = 250 * time.Millisecond
	)

	createVM := func(ctx context.Context, bootOrderDisk, bootOrderIface *uint, efi bool) *kubevirtv1.VirtualMachine {
		vm := &kubevirtv1.VirtualMachine{
			ObjectMeta: metav1.ObjectMeta{Name: vmName, Namespace: namespace},
			Spec: kubevirtv1.VirtualMachineSpec{
				RunStrategy: ptr.To(kubevirtv1.RunStrategyAlways),
				Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
					Spec: kubevirtv1.VirtualMachineInstanceSpec{
						Domain: kubevirtv1.DomainSpec{
							Devices: kubevirtv1.Devices{
								Disks:      []kubevirtv1.Disk{{Name: "disk1", BootOrder: bootOrderDisk}},
								Interfaces: []kubevirtv1.Interface{{Name: "iface1", BootOrder: bootOrderIface}},
							},
						},
						Volumes: []kubevirtv1.Volume{
							{Name: "disk1", VolumeSource: kubevirtv1.VolumeSource{
								EmptyDisk: &kubevirtv1.EmptyDiskSource{Capacity: resource.MustParse("1Gi")},
							}},
						},
						Networks: []kubevirtv1.Network{
							{Name: "iface1", NetworkSource: kubevirtv1.NetworkSource{Pod: &kubevirtv1.PodNetwork{}}},
						},
					},
				},
			},
		}
		if efi {
			vm.Spec.Template.Spec.Domain.Firmware = &kubevirtv1.Firmware{
				Bootloader: &kubevirtv1.Bootloader{EFI: &kubevirtv1.EFI{}},
			}
		}
		Expect(k8sClient.Create(ctx, vm)).To(Succeed())
		return vm
	}

	// createBMC creates the VirtualMachineBMC and, when override is non-nil,
	// writes status.bootOverride via the status subresource (a plain Create
	// drops status when the subresource is enabled).
	createBMC := func(ctx context.Context, override *bmcv1.BootOverrideStatus) *bmcv1.VirtualMachineBMC {
		bmc := &bmcv1.VirtualMachineBMC{
			ObjectMeta: metav1.ObjectMeta{
				Name:      bmcName,
				Namespace: namespace,
			},
			Spec: bmcv1.VirtualMachineBMCSpec{
				VirtualMachineRef: &corev1.LocalObjectReference{Name: vmName},
				AuthSecretRef:     &corev1.LocalObjectReference{Name: "test-secret"},
			},
		}
		Expect(k8sClient.Create(ctx, bmc)).To(Succeed())
		if override != nil {
			bmc.Status.BootOverride = override
			Expect(k8sClient.Status().Update(ctx, bmc)).To(Succeed())
		}
		return bmc
	}

	createVMI := func(ctx context.Context, uid string) *kubevirtv1.VirtualMachineInstance {
		vmi := &kubevirtv1.VirtualMachineInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmName,
				Namespace: namespace,
				UID:       types.UID(uid),
			},
		}
		Expect(k8sClient.Create(ctx, vmi)).To(Succeed())
		return vmi
	}

	validOverride := func(vmiUID string) *bmcv1.BootOverrideStatus {
		return &bmcv1.BootOverrideStatus{
			Mode:   bmcv1.BootOverrideModeOneshot,
			VMIUID: vmiUID,
			BootOrders: map[string]uint{
				"disk:disk1":       9,
				"interface:iface1": 8,
			},
		}
	}

	// createVMAndVMI creates a VM and a running VMI, returning the VMI's actual UID.
	createVMAndVMI := func(ctx context.Context) (string, *kubevirtv1.VirtualMachine) {
		vm := createVM(ctx, ptr.To[uint](7), ptr.To[uint](6), false)
		vmi := createVMI(ctx, "")
		return string(vmi.UID), vm
	}

	getVM := func(ctx context.Context) *kubevirtv1.VirtualMachine {
		vm := &kubevirtv1.VirtualMachine{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: vmName, Namespace: namespace}, vm)).To(Succeed())
		return vm
	}

	getBMC := func(ctx context.Context) *bmcv1.VirtualMachineBMC {
		bmc := &bmcv1.VirtualMachineBMC{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bmcName, Namespace: namespace}, bmc)).To(Succeed())
		return bmc
	}

	BeforeEach(func() {
		ctx := context.Background()
		_ = k8sClient.Delete(ctx, &kubevirtv1.VirtualMachineInstance{ObjectMeta: metav1.ObjectMeta{Name: vmName, Namespace: namespace}})
		_ = k8sClient.Delete(ctx, &kubevirtv1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: vmName, Namespace: namespace}})
		_ = k8sClient.Delete(ctx, &bmcv1.VirtualMachineBMC{ObjectMeta: metav1.ObjectMeta{Name: bmcName, Namespace: namespace}})
	})

	Context("When a VMI is created", func() {
		It("should do nothing when BMC has no boot override", func() {
			ctx := context.Background()
			createVM(ctx, ptr.To[uint](7), ptr.To[uint](6), false)
			createBMC(ctx, nil)
			createVMI(ctx, "uid-aaa")

			Consistently(func() *uint {
				return getVM(ctx).Spec.Template.Spec.Domain.Devices.Disks[0].BootOrder
			}, 2*time.Second, interval).Should(Equal(ptr.To[uint](7)))
		})

		It("should restore boot order when VMI UID differs from backup", func() {
			ctx := context.Background()
			createVM(ctx, ptr.To[uint](7), ptr.To[uint](6), false)
			createBMC(ctx, validOverride("uid-old"))
			createVMI(ctx, "uid-new")

			Eventually(func() bool {
				return getBMC(ctx).Status.BootOverride == nil
			}, timeout, interval).Should(BeTrue(), "status.bootOverride should be cleared")

			vm := getVM(ctx)
			Expect(vm.Spec.Template.Spec.Domain.Devices.Disks[0].BootOrder).To(Equal(ptr.To[uint](9)))
			Expect(vm.Spec.Template.Spec.Domain.Devices.Interfaces[0].BootOrder).To(Equal(ptr.To[uint](8)))
		})

		It("should not restore when VMI UID matches backup", func() {
			ctx := context.Background()
			uid, _ := createVMAndVMI(ctx)
			createBMC(ctx, validOverride(uid))

			Consistently(func() bool {
				return getBMC(ctx).Status.BootOverride != nil
			}, 2*time.Second, interval).Should(BeTrue())
			Expect(getVM(ctx).Spec.Template.Spec.Domain.Devices.Disks[0].BootOrder).To(Equal(ptr.To[uint](7)))
		})

		It("should clear override when VM is deleted", func() {
			ctx := context.Background()
			createVM(ctx, ptr.To[uint](7), ptr.To[uint](6), false)
			createBMC(ctx, validOverride("uid-old"))
			createVMI(ctx, "uid-new")

			Eventually(func() bool {
				return getBMC(ctx).Status.BootOverride == nil
			}, timeout, interval).Should(BeTrue())

			bmc := getBMC(ctx)
			bmc.Status.BootOverride = validOverride("uid-x")
			Expect(k8sClient.Status().Update(ctx, bmc)).To(Succeed())
			Expect(k8sClient.Delete(ctx, &kubevirtv1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: vmName, Namespace: namespace}})).To(Succeed())

			Eventually(func() bool {
				return getBMC(ctx).Status.BootOverride == nil
			}, timeout, interval).Should(BeTrue())
		})

		It("should restore firmware when backup records a different original firmware", func() {
			ctx := context.Background()
			// Backup says the pre-override firmware was BIOS; VM currently has EFI.
			createVM(ctx, ptr.To[uint](7), ptr.To[uint](6), true)
			override := validOverride("uid-old")
			override.OriginalFirmware = bmcv1.FirmwareTypeLegacy
			createBMC(ctx, override)
			createVMI(ctx, "uid-new")

			Eventually(func() bool {
				vm := getVM(ctx)
				fw := vm.Spec.Template.Spec.Domain.Firmware
				return fw == nil || fw.Bootloader == nil || fw.Bootloader.EFI == nil
			}, timeout, interval).Should(BeTrue(), "firmware should be restored to BIOS")
		})

	})
})
