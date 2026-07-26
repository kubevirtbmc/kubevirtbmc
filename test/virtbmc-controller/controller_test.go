package virtbmccontroller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
	"kubevirt.io/kubevirtbmc/test/util"
)

const (
	webhookServiceName      = "kubevirtbmc-webhook-service"
	webhookServiceNamespace = "kubevirtbmc-system"
	vmDeletionTimeout       = time.Second * 120
)

var (
	serviceAccountName = util.E2EAgentPodName
	roleBindingName    = util.E2EVMName + "-virtbmc-rolebinding"
)

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

		By("waiting for webhook endpoints to be ready")
		util.WaitForWebhookEndpointSlicesReady(ctx, k8sClient, timeout, interval)
	})

	Context("when a VirtualMachineBMC is created with all prerequisites", func() {
		It("should create the VM and Secret upfront", func() {
			vm := util.E2EVM(util.E2ENamespace)
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: vm.Name, Namespace: vm.Namespace}, &kubevirtv1.VirtualMachine{}); err != nil {
				if errors.IsNotFound(err) {
					Expect(k8sClient.Create(ctx, vm)).To(Succeed())
				} else {
					Expect(err).ToNot(HaveOccurred())
				}
			}
			secret := util.E2ESecret(util.E2ENamespace)
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}, &corev1.Secret{}); err != nil {
				if errors.IsNotFound(err) {
					Expect(k8sClient.Create(ctx, secret)).To(Succeed())
				} else {
					Expect(err).ToNot(HaveOccurred())
				}
			}
			By("waiting for the VirtualMachineInstance to reach Running phase")
			Eventually(util.VMIRunning(ctx, k8sClient, util.E2EVMName, util.E2ENamespace), timeout, interval).Should(BeTrue(), "VMI should reach Running state")
		})

		It("should allow creating a VirtualMachineBMC", func() {
			existing := &bmcv1.VirtualMachineBMC{}
			if err := k8sClient.Get(ctx, util.BMCKey(util.E2ENamespace), existing); err != nil {
				if !errors.IsNotFound(err) {
					Expect(err).ToNot(HaveOccurred())
					return
				}
				Expect(k8sClient.Create(ctx, util.E2EBMC(util.E2ENamespace))).To(Succeed())
			}
		})

		It("should create a ServiceAccount and RoleBinding", func() {
			Eventually(func() bool {
				sa := &corev1.ServiceAccount{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: serviceAccountName, Namespace: util.E2ENamespace}, sa) == nil
			}, timeout, interval).Should(BeTrue(), "ServiceAccount should be created")

			Eventually(func() bool {
				rb := &rbacv1.RoleBinding{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: roleBindingName, Namespace: util.E2ENamespace}, rb) == nil
			}, timeout, interval).Should(BeTrue(), "RoleBinding should be created")
		})

		It("should create a ready agent Pod", func() {
			By("verifying the agent Pod exists")
			Eventually(util.PodExists(ctx, k8sClient, util.E2ENamespace), timeout, interval).Should(BeTrue(), "agent Pod should be created")
			By("verifying the agent Pod is Running")
			Eventually(util.PodRunningAndReady(ctx, k8sClient, util.E2ENamespace), timeout, interval).Should(BeTrue(), "agent Pod should become Running and Ready")
		})

		It("should create an agent Service", func() {
			Eventually(func() bool {
				svc := &corev1.Service{}
				return k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), svc) == nil
			}, timeout, interval).Should(BeTrue(), "agent Service should be created")
		})

		It("should report all status conditions as True", func() {
			Eventually(func() bool {
				var bmc bmcv1.VirtualMachineBMC
				if err := k8sClient.Get(ctx, util.BMCKey(util.E2ENamespace), &bmc); err != nil {
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
				types.NamespacedName{Name: util.E2EVMName, Namespace: util.E2ENamespace},
				vm,
			)).To(Succeed())

			Expect(k8sClient.Delete(ctx, vm)).To(Succeed())

			By("waiting for the VirtualMachine to be fully deleted")
			Eventually(util.VMNotFound(ctx, k8sClient, util.E2EVMName, util.E2ENamespace), vmDeletionTimeout, interval).Should(BeTrue(),
				"VirtualMachine should be fully deleted")

			By("verifying the agent Pod is removed")
			Eventually(util.PodNotFound(ctx, k8sClient, util.E2ENamespace), vmDeletionTimeout, interval).Should(BeTrue(),
				"agent Pod should be deleted when VM is gone")

			By("verifying VirtualMachineAvailable=False with reason VirtualMachineNotFound")
			Eventually(
				util.HasBMCCondition(ctx, k8sClient, util.E2ENamespace,
					bmcv1.ConditionVirtualMachineAvailable,
					metav1.ConditionFalse,
					"VirtualMachineNotFound"),
				vmDeletionTimeout, interval,
			).Should(BeTrue())
		})

		It("should restore VirtualMachineAvailable=True when VM is re-created", func() {

			By("re-creating the VirtualMachine")
			Expect(k8sClient.Create(ctx, util.E2EVM(util.E2ENamespace))).To(Succeed())

			By("waiting for the VirtualMachineInstance to reach Running phase")
			Eventually(util.VMIRunning(ctx, k8sClient, util.E2EVMName, util.E2ENamespace), timeout, interval).Should(BeTrue(),
				"VMI should reach Running state")

			By("verifying VirtualMachineAvailable becomes True")
			Eventually(
				util.HasBMCCondition(ctx, k8sClient, util.E2ENamespace,
					bmcv1.ConditionVirtualMachineAvailable,
					metav1.ConditionTrue,
					""),
				timeout, interval,
			).Should(BeTrue())

			By("verifying the agent Pod is re-created and running")
			Eventually(util.PodRunningAndReady(ctx, k8sClient, util.E2ENamespace), timeout, interval).Should(BeTrue(), "agent Pod should become Running and Ready")

		})
	})

	Context("when the Secret is deleted", func() {
		It("should delete the agent Pod and set SecretAvailable=False", func() {
			By("deleting the Secret")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: util.E2ESecretName, Namespace: util.E2ENamespace}, secret)).To(Succeed())
			Expect(k8sClient.Delete(ctx, secret)).To(Succeed())

			By("verifying the agent Pod is removed")
			Eventually(util.PodNotFound(ctx, k8sClient, util.E2ENamespace), timeout, interval).Should(BeTrue(),
				"agent Pod should be deleted when Secret is gone")

			By("verifying SecretAvailable=False with reason SecretNotFound")
			Eventually(
				util.HasBMCCondition(ctx, k8sClient, util.E2ENamespace, bmcv1.ConditionSecretAvailable, metav1.ConditionFalse, "SecretNotFound"),
				timeout, interval,
			).Should(BeTrue())
		})

		It("should restore SecretAvailable=True and bring the agent Pod back because both VM and Secret exist", func() {
			By("re-creating the Secret")
			Expect(k8sClient.Create(ctx, util.E2ESecret(util.E2ENamespace))).To(Succeed())

			By("verifying SecretAvailable becomes True")
			Eventually(
				util.HasBMCCondition(ctx, k8sClient, util.E2ENamespace, bmcv1.ConditionSecretAvailable, metav1.ConditionTrue, ""),
				timeout, interval,
			).Should(BeTrue())

			By("verifying the agent Pod is re-created and running")
			Eventually(util.PodRunningAndReady(ctx, k8sClient, util.E2ENamespace), timeout, interval).Should(BeTrue(), "agent Pod should become Running and Ready")
		})
	})

	Context("when the Secret is modified", func() {
		It("should delete the agent Pod and let the controller recreate it", func() {
			By("verifying the agent Pod is running before the change")
			Eventually(util.PodRunningAndReady(ctx, k8sClient, util.E2ENamespace), timeout, interval).Should(BeTrue(), "agent Pod should become Running and Ready")

			var podBefore corev1.Pod
			Expect(k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &podBefore)).To(Succeed())
			originalUID := podBefore.UID

			By("modifying the Secret")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: util.E2ESecretName, Namespace: util.E2ENamespace}, secret)).To(Succeed())
			secret.Data["password"] = []byte("new-password")
			Expect(k8sClient.Update(ctx, secret)).To(Succeed())

			By("verifying the controller reacted: pod is removed and/or recreated with new UID")
			Eventually(func() bool {
				var pod corev1.Pod
				if err := k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &pod); err != nil {
					return errors.IsNotFound(err)
				}
				return pod.UID != originalUID
			}, timeout, interval).Should(BeTrue(), "agent Pod should be deleted or recreated when Secret is modified")

			By("verifying the agent Pod is running after controller reconciles")
			Eventually(util.PodRunningAndReady(ctx, k8sClient, util.E2ENamespace), timeout, interval).Should(BeTrue(), "agent Pod should become Running and Ready")
		})
	})

	Context("when enableIPMI is toggled from default (disabled)", func() {
		JustBeforeEach(func() {
			By("ensuring the BMC starts with IPMI disabled")
			bmcReset := &bmcv1.VirtualMachineBMC{}
			Expect(k8sClient.Get(ctx, util.BMCKey(util.E2ENamespace), bmcReset)).To(Succeed())
			bmcReset.Spec.IPMI = nil
			Expect(k8sClient.Update(ctx, bmcReset)).To(Succeed())
			Eventually(func() bool {
				var pod corev1.Pod
				if err := k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &pod); err != nil {
					return false
				}
				return len(pod.Spec.Containers) == 1 && len(pod.Spec.Containers[0].Ports) == 1
			}, timeout, interval).Should(BeTrue(), "Pod should be recreated without IPMI port")
		})

		It("should recreate Pod and patch Service with IPMI ports", func() {
			By("recording the current Pod and verifying it has no IPMI port")
			var podBefore corev1.Pod
			Expect(k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &podBefore)).To(Succeed())
			Expect(podBefore.Spec.Containers).To(HaveLen(1))
			Expect(podBefore.Spec.Containers[0].Ports).To(HaveLen(1))
			Expect(podBefore.Spec.Containers[0].Ports[0].Name).To(Equal("redfish"))

			By("recording the current Service and verifying it has no IPMI port")
			var svcBefore corev1.Service
			Expect(k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &svcBefore)).To(Succeed())
			Expect(svcBefore.Spec.Ports).To(HaveLen(1))
			Expect(svcBefore.Spec.Ports[0].Name).To(Equal("redfish"))
			svcBeforeClusterIP := svcBefore.Spec.ClusterIP

			By("patching the VirtualMachineBMC to enableIPMI=true")
			bmc := &bmcv1.VirtualMachineBMC{}
			Expect(k8sClient.Get(ctx, util.BMCKey(util.E2ENamespace), bmc)).To(Succeed())
			enabled := true
			bmc.Spec.IPMI = &bmcv1.IPMISpec{Enabled: &enabled}
			Expect(k8sClient.Update(ctx, bmc)).To(Succeed())

			By("waiting for the Pod to be recreated with new UID and IPMI port")
			Eventually(func() bool {
				var pod corev1.Pod
				if err := k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &pod); err != nil {
					return false
				}
				return pod.UID != podBefore.UID &&
					len(pod.Spec.Containers) == 1 &&
					len(pod.Spec.Containers[0].Ports) == 2
			}, timeout, interval).Should(BeTrue(), "Pod should be recreated with IPMI port")

			By("waiting for the Service to be patched with same UID, same ClusterIP, and IPMI port")
			Eventually(func() bool {
				var svc corev1.Service
				if err := k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &svc); err != nil {
					return false
				}
				return svc.UID == svcBefore.UID &&
					svc.Spec.ClusterIP == svcBeforeClusterIP &&
					len(svc.Spec.Ports) == 2
			}, timeout, interval).Should(BeTrue(), "Service should be patched with IPMI port, preserving ClusterIP")
		})
	})
})
