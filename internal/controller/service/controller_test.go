/*
Copyright 2024.

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

package service

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
	ctlvirtualmachinebmc "kubevirt.io/kubevirtbmc/internal/controller/virtualmachinebmc"
)

var _ = Describe("Service Controller", func() {
	const (
		testVirtualMachineBMCName      = "test-vmbmc"
		testVirtualMachineBMCNamespace = "default"
		testVMName                     = "test-vm"
		testClusterIP                  = "10.0.0.100"

		timeout  = time.Second * 10
		duration = time.Second * 10
		interval = time.Millisecond * 250
	)

	Context("When clusterIP for the Service is ready", func() {
		It("Should update the VirtualMachineBMC status with ClusterIP and Ready condition", func() {
			ctx := context.Background()

			By("Creating the referenced VirtualMachine first")
			vm := &kubevirtv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testVMName,
					Namespace: testVirtualMachineBMCNamespace,
				},
				Spec: kubevirtv1.VirtualMachineSpec{
					Running: boolPtr(false),
					Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
						Spec: kubevirtv1.VirtualMachineInstanceSpec{
							Domain: kubevirtv1.DomainSpec{},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, vm)).Should(Succeed())

			By("Creating a new VirtualMachineBMC")
			virtualMachineBMC := &bmcv1.VirtualMachineBMC{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testVirtualMachineBMCName,
					Namespace: testVirtualMachineBMCNamespace,
				},
				TypeMeta: metav1.TypeMeta{
					APIVersion: "bmc.kubevirt.io/v1beta1",
					Kind:       "VirtualMachineBMC",
				},
				Spec: bmcv1.VirtualMachineBMCSpec{
					VirtualMachineRef: &corev1.LocalObjectReference{
						Name: testVMName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, virtualMachineBMC)).To(Succeed())

			By("Creating a Service with ClusterIP and proper label")
			svc := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						bmcv1.VirtualMachineBMCNameLabel: testVirtualMachineBMCName,
					},
					Name:      testVMName + "-virtbmc",
					Namespace: testVirtualMachineBMCNamespace,
				},
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "Service",
				},
				Spec: corev1.ServiceSpec{
					ClusterIP: testClusterIP,
					Ports: []corev1.ServicePort{
						{
							Name: "ipmi",
							Port: ctlvirtualmachinebmc.IPMISvcPort,
						},
						{
							Name: "redfish",
							Port: ctlvirtualmachinebmc.RedfishSvcPort,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, svc)).To(Succeed())

			By("Checking that the VirtualMachineBMC status has ClusterIP updated")
			virtualMachineBMCLookupKey := types.NamespacedName{
				Name:      virtualMachineBMC.Name,
				Namespace: virtualMachineBMC.Namespace,
			}
			updatedVirtualMachineBMC := &bmcv1.VirtualMachineBMC{}

			Eventually(func() bool {
				if err := k8sClient.Get(ctx, virtualMachineBMCLookupKey, updatedVirtualMachineBMC); err != nil {
					return false
				}
				return updatedVirtualMachineBMC.Status.ClusterIP == testClusterIP
			}, timeout, interval).Should(BeTrue())

			By("Checking that the Ready condition is set to True")
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, virtualMachineBMCLookupKey, updatedVirtualMachineBMC); err != nil {
					return false
				}
				for _, cond := range updatedVirtualMachineBMC.Status.Conditions {
					if cond.Type == bmcv1.ConditionReady &&
						cond.Status == metav1.ConditionTrue &&
						cond.Reason == "ServiceReady" {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})

		It("Should not update VirtualMachineBMC status when Service has no VirtualMachineBMC label", func() {
			ctx := context.Background()

			By("Creating a Service without the VirtualMachineBMC label")
			svc := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unlabeled-service",
					Namespace: testVirtualMachineBMCNamespace,
				},
				Spec: corev1.ServiceSpec{
					ClusterIP: "10.0.0.200",
					Ports: []corev1.ServicePort{
						{
							Port: 623,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, svc)).To(Succeed())

			// The reconciler should ignore this service and not error
			// This is a negative test case, so we just ensure no panic/errors
			time.Sleep(time.Second * 2)
		})

		It("Should eventually update VirtualMachineBMC status when ClusterIP is assigned by the cluster", func() {
			ctx := context.Background()

			By("Creating the referenced VirtualMachine")
			vm := &kubevirtv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vm-no-ip",
					Namespace: testVirtualMachineBMCNamespace,
				},
				Spec: kubevirtv1.VirtualMachineSpec{
					Running: boolPtr(false),
					Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
						Spec: kubevirtv1.VirtualMachineInstanceSpec{
							Domain: kubevirtv1.DomainSpec{},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, vm)).Should(Succeed())

			By("Creating a VirtualMachineBMC")
			virtualMachineBMC := &bmcv1.VirtualMachineBMC{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmbmc-no-ip",
					Namespace: testVirtualMachineBMCNamespace,
				},
				Spec: bmcv1.VirtualMachineBMCSpec{
					VirtualMachineRef: &corev1.LocalObjectReference{
						Name: "test-vm-no-ip",
					},
				},
			}
			Expect(k8sClient.Create(ctx, virtualMachineBMC)).To(Succeed())

			By("Creating a Service without ClusterIP (empty)")
			svc := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						bmcv1.VirtualMachineBMCNameLabel: "test-vmbmc-no-ip",
					},
					Name:      "test-vm-no-ip-virtbmc",
					Namespace: testVirtualMachineBMCNamespace,
				},
				Spec: corev1.ServiceSpec{
					ClusterIP: "", // Empty ClusterIP
					Ports: []corev1.ServicePort{
						{
							Name: "ipmi",
							Port: 623,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, svc)).To(Succeed())

			By("Verifying that VirtualMachineBMC status is updated once ClusterIP is assigned")
			virtualMachineBMCLookupKey := types.NamespacedName{
				Name:      "test-vmbmc-no-ip",
				Namespace: testVirtualMachineBMCNamespace,
			}

			updatedVirtualMachineBMC := &bmcv1.VirtualMachineBMC{}
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, virtualMachineBMCLookupKey, updatedVirtualMachineBMC); err != nil {
					return false
				}
				return updatedVirtualMachineBMC.Status.ClusterIP != ""
			}, timeout, interval).Should(BeTrue())
		})
	})
})

func boolPtr(b bool) *bool {
	return &b
}
