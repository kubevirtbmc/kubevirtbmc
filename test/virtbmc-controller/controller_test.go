package virtbmccontroller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
)

const (
	vmName                   = "testvm"
	vmNamespace              = "default"
	bmcName                  = "test-bmc"
	webhookServiceName       = "kubevirtbmc-webhook-service"
	webhookServiceNamespace  = "kubevirtbmc-system"
	secretName               = "bmc-credentials-secret"
	serviceAccountName       = "testvm-virtbmc"
	roleBindingName          = "testvm-virtbmc-rolebinding"
	webhookRegistrationDelay = time.Second * 10
	vmDeletionTimeout        = time.Second * 120
)

func newVM() *kubevirtv1.VirtualMachine {
	runStrategy := kubevirtv1.RunStrategyAlways
	guestMemory := resource.MustParse("256Mi")

	return &kubevirtv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmName,
			Namespace: vmNamespace,
		},
		Spec: kubevirtv1.VirtualMachineSpec{
			RunStrategy: &runStrategy,
			Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
				Spec: kubevirtv1.VirtualMachineInstanceSpec{
					Domain: kubevirtv1.DomainSpec{
						Memory: &kubevirtv1.Memory{
							Guest: &guestMemory,
						},
						Resources: kubevirtv1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse("256Mi"),
							},
						},
						Devices: kubevirtv1.Devices{
							Disks: []kubevirtv1.Disk{
								{
									Name: "containerdisk",
									DiskDevice: kubevirtv1.DiskDevice{
										Disk: &kubevirtv1.DiskTarget{
											Bus: "virtio",
										},
									},
								},
								{
									Name: "cdrom",
									DiskDevice: kubevirtv1.DiskDevice{
										CDRom: &kubevirtv1.CDRomTarget{
											Bus: "sata",
										},
									},
								},
							},
							Interfaces: []kubevirtv1.Interface{
								{
									Name: "default",
									InterfaceBindingMethod: kubevirtv1.InterfaceBindingMethod{
										Masquerade: &kubevirtv1.InterfaceMasquerade{},
									},
								},
							},
						},
					},
					Volumes: []kubevirtv1.Volume{
						{
							Name: "containerdisk",
							VolumeSource: kubevirtv1.VolumeSource{
								ContainerDisk: &kubevirtv1.ContainerDiskSource{
									Image: "quay.io/kubevirt/cirros-container-disk-demo",
								},
							},
						},
						{
							Name: "cdrom",
							VolumeSource: kubevirtv1.VolumeSource{
								EmptyDisk: &kubevirtv1.EmptyDiskSource{
									Capacity: resource.MustParse("1Gi"),
								},
							},
						},
					},
					Networks: []kubevirtv1.Network{
						{
							Name: "default",
							NetworkSource: kubevirtv1.NetworkSource{
								Pod: &kubevirtv1.PodNetwork{},
							},
						},
					},
				},
			},
		},
	}
}

func newSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: vmNamespace},
		Data: map[string][]byte{
			"username": []byte("admin"),
			"password": []byte("password"),
		},
	}
}

func agentPodKey() types.NamespacedName {
	return types.NamespacedName{Name: vmName + "-virtbmc", Namespace: vmNamespace}
}

func bmcKey() types.NamespacedName {
	return types.NamespacedName{Name: bmcName, Namespace: vmNamespace}
}

func hasBMCCondition(ctx context.Context, condType string, status metav1.ConditionStatus, reason string) func() bool {
	return func() bool {
		var bmc bmcv1.VirtualMachineBMC
		if err := k8sClient.Get(ctx, bmcKey(), &bmc); err != nil {
			return false
		}
		for _, c := range bmc.Status.Conditions {
			if c.Type == condType && c.Status == status {
				return reason == "" || c.Reason == reason
			}
		}
		return false
	}
}

func podExists(ctx context.Context) func() bool {
	return func() bool {
		pod := &corev1.Pod{}
		err := k8sClient.Get(ctx, agentPodKey(), pod)
		return err == nil
	}
}

func podNotFound(ctx context.Context) func() bool {
	return func() bool {
		pod := &corev1.Pod{}
		err := k8sClient.Get(ctx, agentPodKey(), pod)
		return errors.IsNotFound(err)
	}
}

func podRunningAndReady(ctx context.Context) func() bool {
	return func() bool {
		pod := &corev1.Pod{}
		if err := k8sClient.Get(ctx, agentPodKey(), pod); err != nil {
			return false
		}

		if pod.Status.Phase != corev1.PodRunning {
			return false
		}

		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady {
				return cond.Status == corev1.ConditionTrue
			}
		}

		return false
	}
}

func vmiPhase(ctx context.Context, name, namespace string, phase kubevirtv1.VirtualMachineInstancePhase) func() bool {
	return func() bool {
		vmi := &kubevirtv1.VirtualMachineInstance{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, vmi)
		if err != nil {
			return false
		}
		return vmi.Status.Phase == phase
	}
}

func vmiRunning(ctx context.Context, name, namespace string) func() bool {
	return vmiPhase(ctx, name, namespace, kubevirtv1.Running)
}

func vmNotFound(ctx context.Context, name, namespace string) func() bool {
	return func() bool {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &kubevirtv1.VirtualMachine{})
		return errors.IsNotFound(err)
	}
}

var _ = Describe("KubeVirtBMC controller manager", Ordered, func() {
	ctx := context.Background()

	It("should run successfully", func() {
		By("validating at least one controller-manager pod exists (may be multiple during rolling updates)")
		var podList corev1.PodList
		Expect(k8sClient.List(ctx, &podList, &client.ListOptions{
			LabelSelector: labels.SelectorFromSet(labels.Set{"control-plane": "controller-manager"}),
		})).To(Succeed())
		Expect(podList.Items).ToNot(BeEmpty(), "expected at least one controller-manager pod")

		By("validating at least one controller-manager pod is ready")
		Eventually(func() bool {
			var list corev1.PodList
			if err := k8sClient.List(ctx, &list, &client.ListOptions{
				LabelSelector: labels.SelectorFromSet(labels.Set{"control-plane": "controller-manager"}),
			}); err != nil {
				return false
			}
			for _, pod := range list.Items {
				for _, c := range pod.Status.Conditions {
					if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
						return true
					}
				}
			}
			return false
		}, timeout, interval).Should(BeTrue(), "at least one controller-manager pod should become Ready")
	})

	It("should have the webhook service ready", func() {
		webhookKey := types.NamespacedName{Name: webhookServiceName, Namespace: webhookServiceNamespace}

		Eventually(func() bool {
			var svc corev1.Service
			if err := k8sClient.Get(ctx, webhookKey, &svc); err != nil {
				return false
			}
			var podList corev1.PodList
			if err := k8sClient.List(ctx, &podList, &client.ListOptions{
				Namespace:     webhookKey.Namespace,
				LabelSelector: labels.SelectorFromSet(svc.Spec.Selector),
			}); err != nil {
				return false
			}
			for _, pod := range podList.Items {
				for _, c := range pod.Status.Conditions {
					if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
						return true
					}
				}
			}
			return false
		}, timeout, interval).Should(BeTrue(), "webhook service should have at least one ready pod")

		By("waiting for webhook registration to propagate")
		time.Sleep(webhookRegistrationDelay)
	})

	Context("when a VirtualMachineBMC is created with all prerequisites", func() {
		It("should create the VM and Secret upfront", func() {
			vm := newVM()
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: vm.Name, Namespace: vm.Namespace}, &kubevirtv1.VirtualMachine{}); err != nil {
				if errors.IsNotFound(err) {
					Expect(k8sClient.Create(ctx, vm)).To(Succeed())
				} else {
					Expect(err).ToNot(HaveOccurred())
				}
			}
			secret := newSecret()
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}, &corev1.Secret{}); err != nil {
				if errors.IsNotFound(err) {
					Expect(k8sClient.Create(ctx, secret)).To(Succeed())
				} else {
					Expect(err).ToNot(HaveOccurred())
				}
			}
			By("waiting for the VirtualMachineInstance to reach Running phase")
			Eventually(vmiRunning(ctx, vmName, vmNamespace), timeout, interval).Should(BeTrue(), "VMI should reach Running state")
		})

		It("should allow creating a VirtualMachineBMC", func() {
			existing := &bmcv1.VirtualMachineBMC{}
			if err := k8sClient.Get(ctx, bmcKey(), existing); err != nil {
				if !errors.IsNotFound(err) {
					Expect(err).ToNot(HaveOccurred())
					return
				}
				bmc := &bmcv1.VirtualMachineBMC{
					ObjectMeta: metav1.ObjectMeta{Name: bmcName, Namespace: vmNamespace},
					Spec: bmcv1.VirtualMachineBMCSpec{
						VirtualMachineRef: &corev1.LocalObjectReference{Name: vmName},
						AuthSecretRef:     &corev1.LocalObjectReference{Name: secretName},
					},
				}
				Expect(k8sClient.Create(ctx, bmc)).To(Succeed())
			}
		})

		It("should create a ServiceAccount and RoleBinding", func() {
			Eventually(func() bool {
				sa := &corev1.ServiceAccount{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: serviceAccountName, Namespace: vmNamespace}, sa) == nil
			}, timeout, interval).Should(BeTrue(), "ServiceAccount should be created")

			Eventually(func() bool {
				rb := &rbacv1.RoleBinding{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: roleBindingName, Namespace: vmNamespace}, rb) == nil
			}, timeout, interval).Should(BeTrue(), "RoleBinding should be created")
		})

		It("should create a ready agent Pod", func() {
			By("verifying the agent Pod exists")
			Eventually(podExists(ctx), timeout, interval).Should(BeTrue(), "agent Pod should be created")
			By("verifying the agent Pod is Running")
			Eventually(podRunningAndReady(ctx), timeout, interval).Should(BeTrue(), "agent Pod should become Running and Ready")
		})

		It("should create an agent Service", func() {
			Eventually(func() bool {
				svc := &corev1.Service{}
				return k8sClient.Get(ctx, agentPodKey(), svc) == nil
			}, timeout, interval).Should(BeTrue(), "agent Service should be created")
		})

		It("should report all status conditions as True", func() {
			Eventually(func() bool {
				var bmc bmcv1.VirtualMachineBMC
				if err := k8sClient.Get(ctx, bmcKey(), &bmc); err != nil {
					return false
				}
				vmAvail, secretAvail, ready := false, false, false
				for _, c := range bmc.Status.Conditions {
					switch c.Type {
					case bmcv1.ConditionVirtualMachineAvailable:
						vmAvail = c.Status == metav1.ConditionTrue
					case bmcv1.ConditionSecretAvailable:
						secretAvail = c.Status == metav1.ConditionTrue
					case bmcv1.ConditionReady:
						ready = c.Status == metav1.ConditionTrue
					}
				}
				return vmAvail && secretAvail && ready
			}, timeout, interval).Should(BeTrue(), "all BMC status conditions should be True")
		})
	})

	Context("when the VirtualMachine is deleted", func() {

		It("should delete the agent Pod and set VirtualMachineAvailable=False", func() {
			By("deleting the VirtualMachine")

			vm := &kubevirtv1.VirtualMachine{}
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Name: vmName, Namespace: vmNamespace},
				vm,
			)).To(Succeed())

			Expect(k8sClient.Delete(ctx, vm)).To(Succeed())

			By("waiting for the VirtualMachine to be fully deleted")
			Eventually(vmNotFound(ctx, vmName, vmNamespace), vmDeletionTimeout, interval).Should(BeTrue(),
				"VirtualMachine should be fully deleted")

			By("verifying the agent Pod is removed")
			Eventually(podNotFound(ctx), vmDeletionTimeout, interval).Should(BeTrue(),
				"agent Pod should be deleted when VM is gone")

			By("verifying VirtualMachineAvailable=False with reason VirtualMachineNotFound")
			Eventually(
				hasBMCCondition(ctx,
					bmcv1.ConditionVirtualMachineAvailable,
					metav1.ConditionFalse,
					"VirtualMachineNotFound"),
				vmDeletionTimeout, interval,
			).Should(BeTrue())
		})

		It("should restore VirtualMachineAvailable=True when VM is re-created", func() {

			By("re-creating the VirtualMachine")
			Expect(k8sClient.Create(ctx, newVM())).To(Succeed())

			By("waiting for the VirtualMachineInstance to reach Running phase")
			Eventually(vmiRunning(ctx, vmName, vmNamespace), timeout, interval).Should(BeTrue(),
				"VMI should reach Running state")

			By("verifying VirtualMachineAvailable becomes True")
			Eventually(
				hasBMCCondition(ctx,
					bmcv1.ConditionVirtualMachineAvailable,
					metav1.ConditionTrue,
					""),
				timeout, interval,
			).Should(BeTrue())

			By("verifying the agent Pod is re-created and running")
			Eventually(podRunningAndReady(ctx), timeout, interval).Should(BeTrue(), "agent Pod should become Running and Ready")

		})
	})

	Context("when the Secret is deleted", func() {
		It("should delete the agent Pod and set SecretAvailable=False", func() {
			By("deleting the Secret")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: vmNamespace}, secret)).To(Succeed())
			Expect(k8sClient.Delete(ctx, secret)).To(Succeed())

			By("verifying the agent Pod is removed")
			Eventually(podNotFound(ctx), timeout, interval).Should(BeTrue(),
				"agent Pod should be deleted when Secret is gone")

			By("verifying SecretAvailable=False with reason SecretNotFound")
			Eventually(
				hasBMCCondition(ctx, bmcv1.ConditionSecretAvailable, metav1.ConditionFalse, "SecretNotFound"),
				timeout, interval,
			).Should(BeTrue())
		})

		It("should restore SecretAvailable=True and bring the agent Pod back because both VM and Secret exist", func() {
			By("re-creating the Secret")
			Expect(k8sClient.Create(ctx, newSecret())).To(Succeed())

			By("verifying SecretAvailable becomes True")
			Eventually(
				hasBMCCondition(ctx, bmcv1.ConditionSecretAvailable, metav1.ConditionTrue, ""),
				timeout, interval,
			).Should(BeTrue())

			By("verifying the agent Pod is re-created and running")
			Eventually(podRunningAndReady(ctx), timeout, interval).Should(BeTrue(), "agent Pod should become Running and Ready")
		})
	})

	Context("when the Secret is modified", func() {
		It("should delete the agent Pod and let the controller recreate it", func() {
			By("verifying the agent Pod is running before the change")
			Eventually(podRunningAndReady(ctx), timeout, interval).Should(BeTrue(), "agent Pod should become Running and Ready")

			var podBefore corev1.Pod
			Expect(k8sClient.Get(ctx, agentPodKey(), &podBefore)).To(Succeed())
			originalUID := podBefore.UID

			By("modifying the Secret")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: vmNamespace}, secret)).To(Succeed())
			secret.Data["password"] = []byte("new-password")
			Expect(k8sClient.Update(ctx, secret)).To(Succeed())

			By("verifying the controller reacted: pod is removed and/or recreated with new UID")
			Eventually(func() bool {
				var pod corev1.Pod
				if err := k8sClient.Get(ctx, agentPodKey(), &pod); err != nil {
					return errors.IsNotFound(err)
				}
				return pod.UID != originalUID
			}, timeout, interval).Should(BeTrue(), "agent Pod should be deleted or recreated when Secret is modified")

			By("verifying the agent Pod is running after controller reconciles")
			Eventually(podRunningAndReady(ctx), timeout, interval).Should(BeTrue(), "agent Pod should become Running and Ready")

			By("restoring the Secret to its original credentials for agent tests")
			restoredSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: vmNamespace}, restoredSecret)).To(Succeed())
			restoredSecret.Data["username"] = []byte("admin")
			restoredSecret.Data["password"] = []byte("password")
			Expect(k8sClient.Update(ctx, restoredSecret)).To(Succeed())
		})
	})
})
