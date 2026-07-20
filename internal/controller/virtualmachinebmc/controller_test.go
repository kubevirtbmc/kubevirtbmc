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

package virtualmachinebmc

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
)

var _ = Describe("VirtualMachineBMC Controller", func() {
	const (
		testVirtualMachineBMCName      = "test-vmbmc"
		testVirtualMachineBMCNamespace = "default"
		testVMName                     = "test-vm"
		testSecretName                 = "test-secret"
		timeout                        = time.Second * 10
		duration                       = time.Second * 10
		interval                       = time.Millisecond * 250
	)

	Context("When creating a VirtualMachineBMC", func() {
		serviceAccountName := fmt.Sprintf("%s-virtbmc", testVMName)
		roleBindingName := fmt.Sprintf("%s-virtbmc-rolebinding", testVMName)

		It("Should create RBAC resources, Pod and Service ", func() {
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

			By("Creating the referenced Secret")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testSecretName,
					Namespace: testVirtualMachineBMCNamespace,
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"username": []byte("admin"),
					"password": []byte("password123"),
				},
			}

			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

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
					AuthSecretRef: &corev1.LocalObjectReference{
						Name: testSecretName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, virtualMachineBMC)).To(Succeed())

			By("Checking that the ServiceAccount is created")
			saLookupKey := types.NamespacedName{
				Name:      serviceAccountName,
				Namespace: testVirtualMachineBMCNamespace,
			}
			createdSA := &corev1.ServiceAccount{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, saLookupKey, createdSA)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Checking that the RoleBinding is created")
			rbLookupKey := types.NamespacedName{
				Name:      roleBindingName,
				Namespace: testVirtualMachineBMCNamespace,
			}
			createdRB := &rbacv1.RoleBinding{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, rbLookupKey, createdRB)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			Expect(createdRB.RoleRef.Name).To(Equal(clusterRoleName))
			Expect(createdRB.Subjects).To(HaveLen(1))
			Expect(createdRB.Subjects[0].Name).To(Equal(serviceAccountName))

			By("Checking that the Pod is created with correct name")
			podLookupKey := types.NamespacedName{
				Name:      testVMName + "-virtbmc",
				Namespace: testVirtualMachineBMCNamespace,
			}
			createdPod := &corev1.Pod{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, podLookupKey, createdPod)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			Expect(createdPod.Labels).To(HaveKeyWithValue(VirtualMachineBMCNameLabel, testVirtualMachineBMCName))
			Expect(createdPod.Labels).To(HaveKeyWithValue(VMNameLabel, testVMName))
			Expect(createdPod.Spec.ServiceAccountName).To(Equal(serviceAccountName))
			Expect(createdPod.Spec.Containers).To(HaveLen(1))
			Expect(createdPod.Spec.Containers[0].Name).To(Equal(virtBMCContainerName))
			Expect(createdPod.Spec.Containers[0].Resources.Requests.Cpu().String()).To(Equal(DefaultAgentCPURequest))
			Expect(createdPod.Spec.Containers[0].Resources.Requests.Memory().String()).To(Equal(DefaultAgentMemoryRequest))

			By("Checking that the Service is created with correct name")
			svcLookupKey := types.NamespacedName{
				Name:      testVMName + "-virtbmc",
				Namespace: testVirtualMachineBMCNamespace,
			}
			createdSvc := &corev1.Service{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, svcLookupKey, createdSvc)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			Expect(createdSvc.Labels).To(HaveKeyWithValue(VirtualMachineBMCNameLabel, testVirtualMachineBMCName))
			Expect(createdSvc.Labels).To(HaveKeyWithValue(VMNameLabel, testVMName))
			Expect(createdSvc.Spec.Selector).To(HaveKeyWithValue(VirtualMachineBMCNameLabel, testVirtualMachineBMCName))
			Expect(createdSvc.Spec.Ports).To(HaveLen(1))
		})

		It("Should set correct status conditions when VirtualMachine and Secret exist", func() {
			ctx := context.Background()

			vmName := "testvm-conditions"
			secretName := "secret-conditions"
			bmcName := "bmc-conditions"

			By("Creating the referenced VirtualMachine")
			vm := &kubevirtv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmName,
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

			By("Creating the referenced Secret")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: testVirtualMachineBMCNamespace,
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"username": []byte("admin"),
					"password": []byte("password123"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating a new VirtualMachineBMC")
			virtualMachineBMC := &bmcv1.VirtualMachineBMC{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bmcName,
					Namespace: testVirtualMachineBMCNamespace,
				},
				Spec: bmcv1.VirtualMachineBMCSpec{
					VirtualMachineRef: &corev1.LocalObjectReference{
						Name: vmName,
					},
					AuthSecretRef: &corev1.LocalObjectReference{
						Name: secretName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, virtualMachineBMC)).To(Succeed())

			By("Checking that VirtualMachineAvailable condition is set to True")
			bmcLookupKey := types.NamespacedName{
				Name:      bmcName,
				Namespace: testVirtualMachineBMCNamespace,
			}
			Eventually(func() bool {
				var bmc bmcv1.VirtualMachineBMC
				if err := k8sClient.Get(ctx, bmcLookupKey, &bmc); err != nil {
					return false
				}
				for _, cond := range bmc.Status.Conditions {
					if cond.Type == bmcv1.ConditionVirtualMachineAvailable &&
						cond.Status == metav1.ConditionTrue &&
						cond.Reason == "VirtualMachineFound" {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			By("Checking that SecretAvailable condition is set to True")
			Eventually(func() bool {
				var bmc bmcv1.VirtualMachineBMC
				if err := k8sClient.Get(ctx, bmcLookupKey, &bmc); err != nil {
					return false
				}
				for _, cond := range bmc.Status.Conditions {
					if cond.Type == bmcv1.ConditionSecretAvailable &&
						cond.Status == metav1.ConditionTrue &&
						cond.Reason == "SecretFound" {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})

		It("Should set status condition when VirtualMachine does not exist", func() {
			ctx := context.Background()

			By("Creating a VirtualMachineBMC without a referenced VM")
			virtualMachineBMC := &bmcv1.VirtualMachineBMC{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmbmc-no-vm",
					Namespace: testVirtualMachineBMCNamespace,
				},
				Spec: bmcv1.VirtualMachineBMCSpec{
					VirtualMachineRef: &corev1.LocalObjectReference{
						Name: "non-existent-vm",
					},
				},
			}
			Expect(k8sClient.Create(ctx, virtualMachineBMC)).To(Succeed())

			By("Checking that the status condition is set")
			bmcLookupKey := types.NamespacedName{
				Name:      "test-vmbmc-no-vm",
				Namespace: testVirtualMachineBMCNamespace,
			}
			Eventually(func() bool {
				var bmc bmcv1.VirtualMachineBMC
				if err := k8sClient.Get(ctx, bmcLookupKey, &bmc); err != nil {
					return false
				}
				for _, cond := range bmc.Status.Conditions {
					if cond.Type == bmcv1.ConditionVirtualMachineAvailable &&
						cond.Status == metav1.ConditionFalse &&
						cond.Reason == "VirtualMachineNotFound" {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})

		It("Should update condition when VirtualMachine is deleted and reconcile", func() {
			ctx := context.Background()

			vmName := "testvm-delete"
			secretName := "secret-delete"
			bmcName := "bmc-vm-delete"

			By("Creating the VirtualMachine and Secret")
			vm := &kubevirtv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmName,
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

			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: testVirtualMachineBMCNamespace,
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"username": []byte("admin"),
					"password": []byte("password123"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating VirtualMachineBMC")
			virtualMachineBMC := &bmcv1.VirtualMachineBMC{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bmcName,
					Namespace: testVirtualMachineBMCNamespace,
				},
				Spec: bmcv1.VirtualMachineBMCSpec{
					VirtualMachineRef: &corev1.LocalObjectReference{
						Name: vmName,
					},
					AuthSecretRef: &corev1.LocalObjectReference{
						Name: secretName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, virtualMachineBMC)).To(Succeed())

			By("Waiting for VirtualMachineAvailable condition to be True")
			bmcLookupKey := types.NamespacedName{
				Name:      bmcName,
				Namespace: testVirtualMachineBMCNamespace,
			}
			Eventually(func() bool {
				var bmc bmcv1.VirtualMachineBMC
				if err := k8sClient.Get(ctx, bmcLookupKey, &bmc); err != nil {
					return false
				}
				for _, cond := range bmc.Status.Conditions {
					if cond.Type == bmcv1.ConditionVirtualMachineAvailable &&
						cond.Status == metav1.ConditionTrue {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			By("Waiting for Pod to be created")
			podLookupKey := types.NamespacedName{
				Name:      vmName + "-virtbmc",
				Namespace: testVirtualMachineBMCNamespace,
			}
			Eventually(func() bool {
				pod := &corev1.Pod{}
				err := k8sClient.Get(ctx, podLookupKey, pod)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Deleting the VirtualMachine")
			vmToDelete := &kubevirtv1.VirtualMachine{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: vmName, Namespace: testVirtualMachineBMCNamespace}, vmToDelete)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, vmToDelete)).Should(Succeed())

			By("Checking that VirtualMachineAvailable condition is updated to False")
			Eventually(func() bool {
				var bmc bmcv1.VirtualMachineBMC
				if err := k8sClient.Get(ctx, bmcLookupKey, &bmc); err != nil {
					return false
				}
				for _, cond := range bmc.Status.Conditions {
					if cond.Type == bmcv1.ConditionVirtualMachineAvailable &&
						cond.Status == metav1.ConditionFalse &&
						cond.Reason == "VirtualMachineNotFound" {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			By("Verifying that reconcile function was triggered")
			// The reconcile should have been triggered by the VM deletion watch
			// We verify this by checking the condition was updated
			var bmc bmcv1.VirtualMachineBMC
			Expect(k8sClient.Get(ctx, bmcLookupKey, &bmc)).Should(Succeed())
			foundCondition := false
			for _, cond := range bmc.Status.Conditions {
				if cond.Type == bmcv1.ConditionVirtualMachineAvailable &&
					cond.Status == metav1.ConditionFalse {
					foundCondition = true
					break
				}
			}
			Expect(foundCondition).To(BeTrue())
		})

		It("Should update condition and delete Pod when Secret is deleted", func() {
			ctx := context.Background()

			vmName := "testvm-secret-delete"
			secretName := "secret-to-delete"
			bmcName := "bmc-secret-delete"

			By("Creating the VirtualMachine and Secret")
			vm := &kubevirtv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmName,
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

			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: testVirtualMachineBMCNamespace,
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"username": []byte("admin"),
					"password": []byte("password123"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating VirtualMachineBMC")
			virtualMachineBMC := &bmcv1.VirtualMachineBMC{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bmcName,
					Namespace: testVirtualMachineBMCNamespace,
				},
				Spec: bmcv1.VirtualMachineBMCSpec{
					VirtualMachineRef: &corev1.LocalObjectReference{
						Name: vmName,
					},
					AuthSecretRef: &corev1.LocalObjectReference{
						Name: secretName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, virtualMachineBMC)).To(Succeed())

			By("Waiting for SecretAvailable condition to be True")
			bmcLookupKey := types.NamespacedName{
				Name:      bmcName,
				Namespace: testVirtualMachineBMCNamespace,
			}
			Eventually(func() bool {
				var bmc bmcv1.VirtualMachineBMC
				if err := k8sClient.Get(ctx, bmcLookupKey, &bmc); err != nil {
					return false
				}
				for _, cond := range bmc.Status.Conditions {
					if cond.Type == bmcv1.ConditionSecretAvailable &&
						cond.Status == metav1.ConditionTrue {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			By("Waiting for Pod to be created")
			podLookupKey := types.NamespacedName{
				Name:      vmName + "-virtbmc",
				Namespace: testVirtualMachineBMCNamespace,
			}
			Eventually(func() bool {
				pod := &corev1.Pod{}
				err := k8sClient.Get(ctx, podLookupKey, pod)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Deleting the Secret")
			secretToDelete := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: testVirtualMachineBMCNamespace}, secretToDelete)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, secretToDelete)).Should(Succeed())

			By("Checking that SecretAvailable condition is updated to False")
			Eventually(func() bool {
				var bmc bmcv1.VirtualMachineBMC
				if err := k8sClient.Get(ctx, bmcLookupKey, &bmc); err != nil {
					return false
				}
				for _, cond := range bmc.Status.Conditions {
					if cond.Type == bmcv1.ConditionSecretAvailable &&
						cond.Status == metav1.ConditionFalse &&
						cond.Reason == "SecretNotFound" {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			By("Verifying that the Pod is deleted")
			Eventually(func() bool {
				pod := &corev1.Pod{}
				err := k8sClient.Get(ctx, podLookupKey, pod)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue())
		})

		It("Should delete and recreate Pod when Secret is changed", func() {
			ctx := context.Background()

			vmName := "testvm-secret-change"
			secretName := "secret-to-change"
			bmcName := "bmc-secret-change"

			By("Creating the VirtualMachine and Secret")
			vm := &kubevirtv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmName,
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

			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: testVirtualMachineBMCNamespace,
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"username": []byte("admin"),
					"password": []byte("password123"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating VirtualMachineBMC")
			virtualMachineBMC := &bmcv1.VirtualMachineBMC{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bmcName,
					Namespace: testVirtualMachineBMCNamespace,
				},
				Spec: bmcv1.VirtualMachineBMCSpec{
					VirtualMachineRef: &corev1.LocalObjectReference{
						Name: vmName,
					},
					AuthSecretRef: &corev1.LocalObjectReference{
						Name: secretName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, virtualMachineBMC)).To(Succeed())

			By("Waiting for Pod to be created")
			podLookupKey := types.NamespacedName{
				Name:      vmName + "-virtbmc",
				Namespace: testVirtualMachineBMCNamespace,
			}
			var originalPod corev1.Pod
			Eventually(func() bool {
				err := k8sClient.Get(ctx, podLookupKey, &originalPod)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			originalPodUID := originalPod.UID

			By("Updating the Secret with new credentials")
			updatedSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: testVirtualMachineBMCNamespace}, updatedSecret)).Should(Succeed())
			updatedSecret.Data = map[string][]byte{
				"username": []byte("newadmin"),
				"password": []byte("newpassword456"),
			}
			Expect(k8sClient.Update(ctx, updatedSecret)).Should(Succeed())

			By("Verifying that the old Pod is deleted")
			Eventually(func() bool {
				pod := &corev1.Pod{}
				err := k8sClient.Get(ctx, podLookupKey, pod)
				if err != nil {
					return errors.IsNotFound(err)
				}
				// Check if it's a different pod (new UID)
				return pod.UID != originalPodUID
			}, timeout, interval).Should(BeTrue())

			By("Verifying that a new Pod is created by the controller")
			Eventually(func() bool {
				pod := &corev1.Pod{}
				err := k8sClient.Get(ctx, podLookupKey, pod)
				if err != nil {
					return false
				}
				// Verify it's a new pod with different UID
				return pod.UID != originalPodUID
			}, timeout, interval).Should(BeTrue())

			By("Verifying the new Pod has correct labels and service account")
			var newPod corev1.Pod
			Expect(k8sClient.Get(ctx, podLookupKey, &newPod)).Should(Succeed())
			Expect(newPod.Labels).To(HaveKeyWithValue(VirtualMachineBMCNameLabel, bmcName))
			Expect(newPod.Labels).To(HaveKeyWithValue(VMNameLabel, vmName))
			Expect(newPod.Spec.ServiceAccountName).To(Equal(vmName + "-virtbmc"))
		})

		It("Should delete and recreate Pod and patch Service when enableIPMI is changed", func() {
			ctx := context.Background()

			By("Getting VirtualMachine and Secret from the first test")
			podLookupKey := types.NamespacedName{
				Name:      testVMName + "-virtbmc",
				Namespace: testVirtualMachineBMCNamespace,
			}
			svcLookupKey := types.NamespacedName{
				Name:      testVMName + "-virtbmc",
				Namespace: testVirtualMachineBMCNamespace,
			}
			bmcLookupKey := types.NamespacedName{
				Name:      testVirtualMachineBMCName,
				Namespace: testVirtualMachineBMCNamespace,
			}

			var originalPod corev1.Pod
			var originalSvc corev1.Service
			Eventually(func() bool {
				err1 := k8sClient.Get(ctx, podLookupKey, &originalPod)
				err2 := k8sClient.Get(ctx, svcLookupKey, &originalSvc)
				return err1 == nil && err2 == nil
			}, timeout, interval).Should(BeTrue())

			originalPodUID := originalPod.UID
			originalSvcUID := originalSvc.UID
			originalSvcClusterIP := originalSvc.Spec.ClusterIP

			Expect(originalPod.Spec.Containers[0].Ports).To(HaveLen(1))
			Expect(originalPod.Spec.Containers[0].Ports[0].Name).To(Equal(redfishPortName))

			Expect(originalSvc.Spec.Ports).To(HaveLen(1))
			Expect(originalSvc.Spec.Ports[0].Name).To(Equal(redfishPortName))

			By("Updating the existing VirtualMachineBMC to enableIPMI=true")
			updatedBMC := &bmcv1.VirtualMachineBMC{}
			Expect(k8sClient.Get(ctx, bmcLookupKey, updatedBMC)).Should(Succeed())
			updatedBMC.Spec.IPMI = &bmcv1.IPMISpec{Enabled: boolPtr(true)}
			Expect(k8sClient.Update(ctx, updatedBMC)).Should(Succeed())

			By("Verifying that Pod is recreated (new UID) and Service is patched (same UID) with IPMI ports")
			var newPod corev1.Pod
			var newSvc corev1.Service
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, podLookupKey, &newPod); err != nil {
					return false
				}
				if err := k8sClient.Get(ctx, svcLookupKey, &newSvc); err != nil {
					return false
				}
				if newPod.UID == originalPodUID {
					return false
				}
				if newSvc.UID != originalSvcUID {
					return false
				}
				if len(newPod.Spec.Containers[0].Ports) < 2 || len(newSvc.Spec.Ports) < 2 {
					return false
				}
				return true
			}, timeout, interval).Should(BeTrue())

			Expect(newPod.Spec.Containers[0].Ports).To(HaveLen(2))
			portNames := []string{newPod.Spec.Containers[0].Ports[0].Name, newPod.Spec.Containers[0].Ports[1].Name}
			Expect(portNames).To(ContainElement(redfishPortName))
			Expect(portNames).To(ContainElement(ipmiPortName))

			Expect(newSvc.Spec.ClusterIP).To(Equal(originalSvcClusterIP))
			Expect(newSvc.Spec.Ports).To(HaveLen(2))
			svcPortNames := []string{newSvc.Spec.Ports[0].Name, newSvc.Spec.Ports[1].Name}
			Expect(svcPortNames).To(ContainElement(redfishPortName))
			Expect(svcPortNames).To(ContainElement(ipmiPortName))
		})

	})
})

func boolPtr(b bool) *bool {
	return &b
}
