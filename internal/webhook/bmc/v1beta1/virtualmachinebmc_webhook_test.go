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

package v1beta1

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1beta1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
)

func boolPtr(b bool) *bool {
	return &b
}

var _ = Describe("VirtualMachineBMC Webhook", func() {
	var (
		ctx        context.Context
		obj        *bmcv1beta1.VirtualMachineBMC
		oldObj     *bmcv1beta1.VirtualMachineBMC
		validator  VirtualMachineBMCCustomValidator
		defaulter  VirtualMachineBMCCustomDefaulter
		namespace  string
		vmName     string
		secretName string
	)

	BeforeEach(func() {
		ctx = context.Background()
		namespace = "default"
		vmName = "test-vm"
		secretName = "test-secret"

		obj = &bmcv1beta1.VirtualMachineBMC{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-vmbmc",
				Namespace: namespace,
			},
		}
		oldObj = &bmcv1beta1.VirtualMachineBMC{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-vmbmc",
				Namespace: namespace,
			},
		}
		validator = VirtualMachineBMCCustomValidator{
			Client: k8sClient,
		}
		defaulter = VirtualMachineBMCCustomDefaulter{}
	})

	AfterEach(func() {
		// Clean up test resources
		vm := &kubevirtv1.VirtualMachine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmName,
				Namespace: namespace,
			},
		}
		_ = k8sClient.Delete(ctx, vm)

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: namespace,
			},
		}
		_ = k8sClient.Delete(ctx, secret)
	})

	Context("When creating VirtualMachineBMC under Defaulting Webhook", func() {
		It("Should not modify the object when defaults are not needed", func() {
			By("calling the Default method")
			originalSpec := obj.Spec.DeepCopy()
			err := defaulter.Default(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
			Expect(obj.Spec).To(Equal(*originalSpec))
		})
	})

	Context("When creating VirtualMachineBMC under Validating Webhook", func() {
		It("Should deny creation if VirtualMachineRef is nil", func() {
			By("creating a VirtualMachineBMC without VirtualMachineRef")
			obj.Spec.VirtualMachineRef = nil

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.virtualMachineRef is required"))
		})

		It("Should deny creation if VirtualMachineRef.Name is empty", func() {
			By("creating a VirtualMachineBMC with empty VirtualMachineRef.Name")
			obj.Spec.VirtualMachineRef = &corev1.LocalObjectReference{
				Name: "",
			}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.virtualMachineRef.name cannot be empty"))
		})

		It("Should deny creation if VirtualMachine does not exist", func() {
			By("creating a VirtualMachineBMC referencing a non-existent VirtualMachine")
			obj.Spec.VirtualMachineRef = &corev1.LocalObjectReference{
				Name: "non-existent-vm",
			}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found in namespace"))
		})

		It("Should deny creation if AuthSecretRef is provided but Secret does not exist", func() {
			By("creating a VirtualMachine first")
			vm := &kubevirtv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmName,
					Namespace: namespace,
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

			By("creating a VirtualMachineBMC with non-existent Secret reference")
			obj.Spec.VirtualMachineRef = &corev1.LocalObjectReference{
				Name: vmName,
			}
			obj.Spec.AuthSecretRef = &corev1.LocalObjectReference{
				Name: "non-existent-secret",
			}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found in namespace"))
		})

		It("Should deny creation if AuthSecretRef.Name is empty", func() {
			By("creating a VirtualMachine first")
			vm := &kubevirtv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmName,
					Namespace: namespace,
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

			By("creating a VirtualMachineBMC with empty AuthSecretRef.Name")
			obj.Spec.VirtualMachineRef = &corev1.LocalObjectReference{
				Name: vmName,
			}
			obj.Spec.AuthSecretRef = &corev1.LocalObjectReference{
				Name: "",
			}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.authSecretRef.name cannot be empty"))
		})

		It("Should deny creation if AuthSecretRef is not provided", func() {
			By("creating a VirtualMachine first")
			vm := &kubevirtv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmName,
					Namespace: namespace,
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

			By("creating a VirtualMachineBMC with valid VirtualMachineRef but no AuthSecretRef")
			obj.Spec.VirtualMachineRef = &corev1.LocalObjectReference{
				Name: vmName,
			}
			obj.Spec.AuthSecretRef = nil

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.authSecretRef is required"))
		})

		It("Should accept creation if VirtualMachine and Secret both exist", func() {
			By("creating a VirtualMachine first")
			vm := &kubevirtv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmName,
					Namespace: namespace,
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

			By("creating a Secret")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"username": []byte("admin"),
					"password": []byte("password"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("creating a VirtualMachineBMC with valid VirtualMachineRef and AuthSecretRef")
			obj.Spec.VirtualMachineRef = &corev1.LocalObjectReference{
				Name: vmName,
			}
			obj.Spec.AuthSecretRef = &corev1.LocalObjectReference{
				Name: secretName,
			}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("When updating VirtualMachineBMC under Validating Webhook", func() {
		It("Should deny update if VirtualMachineRef is nil", func() {
			By("updating a VirtualMachineBMC without VirtualMachineRef")
			obj.Spec.VirtualMachineRef = nil

			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.virtualMachineRef is required"))
		})

		It("Should deny update if VirtualMachine does not exist", func() {
			By("updating a VirtualMachineBMC referencing a non-existent VirtualMachine")
			obj.Spec.VirtualMachineRef = &corev1.LocalObjectReference{
				Name: "non-existent-vm",
			}

			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found in namespace"))
		})

		It("Should deny update if AuthSecretRef is provided but Secret does not exist", func() {
			By("creating a VirtualMachine first")
			vm := &kubevirtv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmName,
					Namespace: namespace,
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

			By("updating a VirtualMachineBMC with non-existent Secret reference")
			obj.Spec.VirtualMachineRef = &corev1.LocalObjectReference{
				Name: vmName,
			}
			obj.Spec.AuthSecretRef = &corev1.LocalObjectReference{
				Name: "non-existent-secret",
			}

			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found in namespace"))
		})

		It("Should accept update if VirtualMachine and Secret both exist", func() {
			By("creating a VirtualMachine first")
			vm := &kubevirtv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmName,
					Namespace: namespace,
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

			By("creating a Secret")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"username": []byte("admin"),
					"password": []byte("password"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("updating a VirtualMachineBMC with valid VirtualMachineRef and AuthSecretRef")
			obj.Spec.VirtualMachineRef = &corev1.LocalObjectReference{
				Name: vmName,
			}
			obj.Spec.AuthSecretRef = &corev1.LocalObjectReference{
				Name: secretName,
			}

			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("When validating NetworkRef", func() {
		const otherNamespace = "nad-other-ns"

		newNAD := func(namespace, name string) *unstructured.Unstructured {
			nad := &unstructured.Unstructured{}
			nad.SetGroupVersionKind(bmcv1beta1.NetworkAttachmentDefinitionGVK)
			nad.SetNamespace(namespace)
			nad.SetName(name)
			Expect(unstructured.SetNestedField(nad.Object, `{"cniVersion":"0.3.1","type":"bridge"}`, "spec", "config")).To(Succeed())
			return nad
		}

		BeforeEach(func() {
			vm := &kubevirtv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{Name: vmName, Namespace: namespace},
				Spec: kubevirtv1.VirtualMachineSpec{
					Running: boolPtr(false),
					Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
						Spec: kubevirtv1.VirtualMachineInstanceSpec{Domain: kubevirtv1.DomainSpec{}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, vm)).Should(Succeed())
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
				Data: map[string][]byte{
					"username": []byte("admin"),
					"password": []byte("password"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			for _, o := range []*bmcv1beta1.VirtualMachineBMC{obj, oldObj} {
				o.Spec.VirtualMachineRef = &corev1.LocalObjectReference{Name: vmName}
				o.Spec.AuthSecretRef = &corev1.LocalObjectReference{Name: secretName}
			}

			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: otherNamespace}}
			Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, ns))).To(Succeed())
			for _, nad := range []*unstructured.Unstructured{newNAD(namespace, "bmc-net"), newNAD(otherNamespace, "shared-net")} {
				Expect(k8sClient.Create(ctx, nad)).To(Succeed())
				DeferCleanup(func() {
					Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, nad))).To(Succeed())
				})
			}
		})

		It("Should deny a NetworkRef that is not a name or namespace/name", func() {
			for _, networkRef := range []string{`[{"name":"bmc-net"}]`, "a/b/c", "Bmc_Net", "/bmc-net", "default/"} {
				obj.Spec.NetworkRef = networkRef
				_, err := validator.ValidateCreate(ctx, obj)
				Expect(err).To(HaveOccurred(), "networkRef %q should be denied", networkRef)
				Expect(err.Error()).To(ContainSubstring("spec.networkRef"))
			}
		})

		It("Should accept an existing NetworkAttachmentDefinition in the same namespace without warnings", func() {
			for _, networkRef := range []string{"bmc-net", namespace + "/bmc-net"} {
				obj.Spec.NetworkRef = networkRef
				warnings, err := validator.ValidateCreate(ctx, obj)
				Expect(err).NotTo(HaveOccurred())
				Expect(warnings).To(BeEmpty(), "networkRef %q", networkRef)
			}
		})

		It("Should accept a missing NetworkAttachmentDefinition with a warning", func() {
			obj.Spec.NetworkRef = "missing-net"
			warnings, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(ConsistOf(ContainSubstring(`NetworkAttachmentDefinition "default/missing-net" not found`)))
		})

		It("Should accept a cross-namespace NetworkAttachmentDefinition with a warning", func() {
			obj.Spec.NetworkRef = otherNamespace + "/shared-net"
			warnings, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(ConsistOf(ContainSubstring("namespaceIsolation")))
		})

		It("Should only warn on update when NetworkRef changes", func() {
			oldObj.Spec.NetworkRef = "missing-net"
			obj.Spec.NetworkRef = "missing-net"
			warnings, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(BeEmpty())

			obj.Spec.NetworkRef = "other-missing-net"
			warnings, err = validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(ConsistOf(ContainSubstring("not found")))
		})

		It("Should deny an update to a malformed NetworkRef", func() {
			obj.Spec.NetworkRef = "a/b/c"
			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
		})
	})
})
