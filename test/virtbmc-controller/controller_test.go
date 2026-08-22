package virtbmccontroller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
	virtualmachinebmccontroller "kubevirt.io/kubevirtbmc/internal/controller/virtualmachinebmc"
	"kubevirt.io/kubevirtbmc/test/util"
)

const (
	webhookServiceName      = "kubevirtbmc-webhook-service"
	webhookServiceNamespace = "kubevirtbmc-system"
	vmDeletionTimeout       = time.Second * 120
)

var (
	serviceAccountName = util.E2EAgentDeploymentName
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

		It("should create an agent Deployment and mark it ready", func() {
			By("verifying the agent Deployment exists")
			Eventually(util.DeploymentExists(ctx, k8sClient, util.E2ENamespace), timeout, interval).Should(BeTrue(), "agent Deployment should be created")
			By("verifying the agent Deployment becomes ready")
			Eventually(util.DeploymentReady(ctx, k8sClient, util.E2ENamespace), timeout, interval).Should(BeTrue(), "agent Deployment should become ready")
		})

		It("should create an agent Service", func() {
			Eventually(func() bool {
				svc := &corev1.Service{}
				return k8sClient.Get(ctx, util.AgentServiceKey(util.E2ENamespace), svc) == nil
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

		It("should delete the agent Deployment and set VirtualMachineAvailable=False", func() {
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

			By("verifying the agent Deployment is removed")
			Eventually(util.DeploymentNotFound(ctx, k8sClient, util.E2ENamespace), vmDeletionTimeout, interval).Should(BeTrue(),
				"agent Deployment should be deleted when VM is gone")

			By("verifying the agent Service is removed")
			Eventually(util.ServiceNotFound(ctx, k8sClient, util.E2ENamespace), vmDeletionTimeout, interval).Should(BeTrue(),
				"agent Service should be deleted when VM is gone")

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

			By("verifying the agent Deployment is re-created and becomes ready")
			Eventually(util.DeploymentExists(ctx, k8sClient, util.E2ENamespace), timeout, interval).Should(BeTrue(), "agent Deployment should be re-created")
			Eventually(util.DeploymentReady(ctx, k8sClient, util.E2ENamespace), timeout, interval).Should(BeTrue(), "agent Deployment should become ready")

			By("verifying the agent Service is re-created")
			Eventually(func() bool {
				svc := &corev1.Service{}
				return k8sClient.Get(ctx, util.AgentServiceKey(util.E2ENamespace), svc) == nil
			}, timeout, interval).Should(BeTrue(), "agent Service should be re-created")
		})
	})

	Context("when the Secret is deleted", func() {
		It("should delete the agent Deployment and Service and set SecretAvailable=False", func() {
			By("deleting the Secret")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: util.E2ESecretName, Namespace: util.E2ENamespace}, secret)).To(Succeed())
			Expect(k8sClient.Delete(ctx, secret)).To(Succeed())

			By("verifying the agent Deployment is removed")
			Eventually(util.DeploymentNotFound(ctx, k8sClient, util.E2ENamespace), timeout, interval).Should(BeTrue(),
				"agent Deployment should be deleted when Secret is gone")

			By("verifying the agent Service is removed")
			Eventually(util.ServiceNotFound(ctx, k8sClient, util.E2ENamespace), timeout, interval).Should(BeTrue(),
				"agent Service should be deleted when Secret is gone")

			By("verifying SecretAvailable=False with reason SecretNotFound")
			Eventually(
				util.HasBMCCondition(ctx, k8sClient, util.E2ENamespace, bmcv1.ConditionSecretAvailable, metav1.ConditionFalse, "SecretNotFound"),
				timeout, interval,
			).Should(BeTrue())
		})

		It("should restore SecretAvailable=True and bring the agent Pod and Service back because both VM and Secret exist", func() {
			By("re-creating the Secret")
			Expect(k8sClient.Create(ctx, util.E2ESecret(util.E2ENamespace))).To(Succeed())

			By("verifying SecretAvailable becomes True")
			Eventually(
				util.HasBMCCondition(ctx, k8sClient, util.E2ENamespace, bmcv1.ConditionSecretAvailable, metav1.ConditionTrue, ""),
				timeout, interval,
			).Should(BeTrue())

			By("verifying the agent Deployment is re-created and becomes ready")
			Eventually(util.DeploymentExists(ctx, k8sClient, util.E2ENamespace), timeout, interval).Should(BeTrue(), "agent Deployment should be re-created")
			Eventually(util.DeploymentReady(ctx, k8sClient, util.E2ENamespace), timeout, interval).Should(BeTrue(), "agent Deployment should become ready")

			By("verifying the agent Service is re-created")
			Eventually(func() bool {
				svc := &corev1.Service{}
				return k8sClient.Get(ctx, util.AgentServiceKey(util.E2ENamespace), svc) == nil
			}, timeout, interval).Should(BeTrue(), "agent Service should be re-created")
		})
	})

	Context("when the Secret is modified", func() {
		It("should roll out the agent Deployment via the secret-hash annotation", func() {
			By("verifying the agent Pod is running before the change")
			Eventually(util.DeploymentReady(ctx, k8sClient, util.E2ENamespace), timeout, interval).Should(BeTrue(), "agent Deployment should become ready")

			var deploymentBefore appsv1.Deployment
			Expect(k8sClient.Get(ctx, util.AgentDeploymentKey(util.E2ENamespace), &deploymentBefore)).To(Succeed())
			originalSecretHash := ""
			if deploymentBefore.Spec.Template.Annotations != nil {
				originalSecretHash = deploymentBefore.Spec.Template.Annotations[virtualmachinebmccontroller.SecretHashAnnotation]
			}
			Expect(originalSecretHash).NotTo(BeEmpty())

			By("modifying the Secret")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: util.E2ESecretName, Namespace: util.E2ENamespace}, secret)).To(Succeed())
			secret.Data["password"] = []byte("new-password")
			Expect(k8sClient.Update(ctx, secret)).To(Succeed())

			By("verifying the controller reacted by updating the Deployment's secret-hash annotation")
			Eventually(func() bool {
				var deployment appsv1.Deployment
				if err := k8sClient.Get(ctx, util.AgentDeploymentKey(util.E2ENamespace), &deployment); err != nil {
					return false
				}
				if deployment.Spec.Template.Annotations == nil {
					return false
				}
				secretHash := deployment.Spec.Template.Annotations[virtualmachinebmccontroller.SecretHashAnnotation]
				return secretHash != "" && secretHash != originalSecretHash
			}, timeout, interval).Should(BeTrue(), "agent Deployment should get a new secret-hash annotation when Secret is modified")

			By("verifying the agent Pod is running after controller reconciles")
			Eventually(util.DeploymentReady(ctx, k8sClient, util.E2ENamespace), timeout, interval).Should(BeTrue(), "agent Deployment should become ready")
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
				pod, err := util.AgentPod(ctx, k8sClient, util.E2ENamespace)
				if err != nil {
					return false
				}
				return len(pod.Spec.Containers) == 1 && len(pod.Spec.Containers[0].Ports) == 1
			}, timeout, interval).Should(BeTrue(), "Pod should be recreated without IPMI port")
		})

		It("should update the Deployment in place, roll the Pod, and patch the Service with IPMI ports", func() {
			By("recording the current Pod and verifying it has no IPMI port")
			podBefore, err := util.AgentPod(ctx, k8sClient, util.E2ENamespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(podBefore.Spec.Containers).To(HaveLen(1))
			Expect(podBefore.Spec.Containers[0].Ports).To(HaveLen(1))
			Expect(podBefore.Spec.Containers[0].Ports[0].Name).To(Equal("redfish"))

			By("recording the current Deployment UID")
			var deploymentBefore appsv1.Deployment
			Expect(k8sClient.Get(ctx, util.AgentDeploymentKey(util.E2ENamespace), &deploymentBefore)).To(Succeed())
			deploymentBeforeUID := deploymentBefore.UID

			By("recording the current Service and verifying it has no IPMI port")
			var svcBefore corev1.Service
			Expect(k8sClient.Get(ctx, util.AgentServiceKey(util.E2ENamespace), &svcBefore)).To(Succeed())
			Expect(svcBefore.Spec.Ports).To(HaveLen(1))
			Expect(svcBefore.Spec.Ports[0].Name).To(Equal("redfish"))
			svcBeforeClusterIP := svcBefore.Spec.ClusterIP

			By("patching the VirtualMachineBMC to enableIPMI=true")
			bmc := &bmcv1.VirtualMachineBMC{}
			Expect(k8sClient.Get(ctx, util.BMCKey(util.E2ENamespace), bmc)).To(Succeed())
			enabled := true
			bmc.Spec.IPMI = &bmcv1.IPMISpec{Enabled: &enabled}
			Expect(k8sClient.Update(ctx, bmc)).To(Succeed())

			By("waiting for the Pod to be rolled with a new UID and IPMI port")
			Eventually(func() bool {
				pod, err := util.AgentPod(ctx, k8sClient, util.E2ENamespace)
				if err != nil {
					return false
				}
				return pod.UID != podBefore.UID &&
					len(pod.Spec.Containers) == 1 &&
					len(pod.Spec.Containers[0].Ports) == 2
			}, timeout, interval).Should(BeTrue(), "Pod should be rolled with IPMI port")

			By("verifying the Deployment itself was updated in place (same UID), not deleted and recreated")
			var deploymentAfter appsv1.Deployment
			Expect(k8sClient.Get(ctx, util.AgentDeploymentKey(util.E2ENamespace), &deploymentAfter)).To(Succeed())
			Expect(deploymentAfter.UID).To(Equal(deploymentBeforeUID), "Deployment should be updated via Server-Side Apply, not deleted and recreated")

			By("waiting for the Service to be patched with same UID, same ClusterIP, and IPMI port")
			Eventually(func() bool {
				var svc corev1.Service
				if err := k8sClient.Get(ctx, util.AgentServiceKey(util.E2ENamespace), &svc); err != nil {
					return false
				}
				return svc.UID == svcBefore.UID &&
					svc.Spec.ClusterIP == svcBeforeClusterIP &&
					len(svc.Spec.Ports) == 2
			}, timeout, interval).Should(BeTrue(), "Service should be patched with IPMI port, preserving ClusterIP")
		})
	})

	Context("when the BMC Service type is changed", Ordered, func() {
		It("should default to ClusterIP when spec.service is not set", func() {
			By("verifying the underlying Service defaults to type ClusterIP")
			var svc corev1.Service
			Expect(k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &svc)).To(Succeed())
			Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))

			By("verifying the VirtualMachineBMC status reports only ClusterIP")
			Eventually(func() bool {
				var bmc bmcv1.VirtualMachineBMC
				if err := k8sClient.Get(ctx, util.BMCKey(util.E2ENamespace), &bmc); err != nil {
					return false
				}
				return bmc.Status.ClusterIP != "" && bmc.Status.LoadBalancerIP == ""
			}, timeout, interval).Should(BeTrue(), "status should report ClusterIP only when spec.service is unset")
		})

		It("should recreate the Service as LoadBalancer and report a LoadBalancer ingress IP", func() {
			By("recording the current ClusterIP Service before the type change")
			var svcBefore corev1.Service
			Expect(k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &svcBefore)).To(Succeed())
			Expect(svcBefore.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))

			By("switching the VirtualMachineBMC Service type to LoadBalancer")
			bmc := &bmcv1.VirtualMachineBMC{}
			Expect(k8sClient.Get(ctx, util.BMCKey(util.E2ENamespace), bmc)).To(Succeed())
			bmc.Spec.Service = &bmcv1.BMCServiceSpec{Type: corev1.ServiceTypeLoadBalancer}
			Expect(k8sClient.Update(ctx, bmc)).To(Succeed())

			By("verifying the old ClusterIP Service is deleted and/or recreated with a new UID")
			Eventually(func() bool {
				var svc corev1.Service
				if err := k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &svc); err != nil {
					return errors.IsNotFound(err)
				}
				return svc.UID != svcBefore.UID
			}, timeout, interval).Should(BeTrue(), "Service should be deleted when its type changes")

			By("verifying the recreated Service has type LoadBalancer")
			Eventually(func() bool {
				var svc corev1.Service
				if err := k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &svc); err != nil {
					return false
				}
				return svc.Spec.Type == corev1.ServiceTypeLoadBalancer
			}, timeout, interval).Should(BeTrue(), "Service should be recreated with type LoadBalancer")

			By("waiting for cloud-provider-kind to assign a LoadBalancer ingress IP")
			Eventually(func() bool {
				var svc corev1.Service
				if err := k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &svc); err != nil {
					return false
				}
				return len(svc.Status.LoadBalancer.Ingress) > 0 && svc.Status.LoadBalancer.Ingress[0].IP != ""
			}, timeout, interval).Should(BeTrue(), "Service should receive a LoadBalancer ingress IP from cloud-provider-kind")

			By("verifying the VirtualMachineBMC status reports both ClusterIP and LoadBalancerIP")
			Eventually(func() bool {
				var updated bmcv1.VirtualMachineBMC
				if err := k8sClient.Get(ctx, util.BMCKey(util.E2ENamespace), &updated); err != nil {
					return false
				}
				return updated.Status.ClusterIP != "" && updated.Status.LoadBalancerIP != ""
			}, timeout, interval).Should(BeTrue(), "status should report both ClusterIP and LoadBalancerIP once the LoadBalancer is ready")

			By("verifying the ServiceReady condition is True")
			Eventually(
				util.HasBMCCondition(ctx, k8sClient, util.E2ENamespace, bmcv1.ConditionReady, metav1.ConditionTrue, bmcv1.ConditionReady),
				timeout, interval,
			).Should(BeTrue())
		})

		It("should recreate the Service as ClusterIP and clear the LoadBalancerIP when switched back", func() {
			By("recording the current LoadBalancer Service before the type change")
			var svcBefore corev1.Service
			Expect(k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &svcBefore)).To(Succeed())
			Expect(svcBefore.Spec.Type).To(Equal(corev1.ServiceTypeLoadBalancer))

			By("switching the VirtualMachineBMC Service type back to ClusterIP")
			bmc := &bmcv1.VirtualMachineBMC{}
			Expect(k8sClient.Get(ctx, util.BMCKey(util.E2ENamespace), bmc)).To(Succeed())
			Expect(bmc.Spec.Service).ToNot(BeNil())
			bmc.Spec.Service.Type = corev1.ServiceTypeClusterIP
			Expect(k8sClient.Update(ctx, bmc)).To(Succeed())

			By("verifying the LoadBalancer Service is deleted and/or recreated with a new UID")
			Eventually(func() bool {
				var svc corev1.Service
				if err := k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &svc); err != nil {
					return errors.IsNotFound(err)
				}
				return svc.UID != svcBefore.UID
			}, timeout, interval).Should(BeTrue(), "Service should be deleted when its type changes back to ClusterIP")

			By("verifying the recreated Service has type ClusterIP")
			Eventually(func() bool {
				var svc corev1.Service
				if err := k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &svc); err != nil {
					return false
				}
				return svc.Spec.Type == corev1.ServiceTypeClusterIP
			}, timeout, interval).Should(BeTrue(), "Service should be recreated with type ClusterIP")

			By("verifying the VirtualMachineBMC status reports ClusterIP and clears LoadBalancerIP")
			Eventually(func() bool {
				var updated bmcv1.VirtualMachineBMC
				if err := k8sClient.Get(ctx, util.BMCKey(util.E2ENamespace), &updated); err != nil {
					return false
				}
				return updated.Status.ClusterIP != "" && updated.Status.LoadBalancerIP == ""
			}, timeout, interval).Should(BeTrue(), "status should report ClusterIP only once switched back, clearing LoadBalancerIP")

			By("verifying the ServiceReady condition is True")
			Eventually(
				util.HasBMCCondition(ctx, k8sClient, util.E2ENamespace, bmcv1.ConditionReady, metav1.ConditionTrue, bmcv1.ConditionReady),
				timeout, interval,
			).Should(BeTrue())
		})
	})

	Context("when the BMC Service labels and annotations are changed", Ordered, func() {
		It("should patch the Service in-place, without requiring the Service to be manually deleted", func() {
			By("recording the current Service UID before adding labels/annotations")
			var svcBefore corev1.Service
			Expect(k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &svcBefore)).To(Succeed())
			Expect(svcBefore.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))

			By("setting spec.service.labels and spec.service.annotations on the VirtualMachineBMC")
			bmc := &bmcv1.VirtualMachineBMC{}
			Expect(k8sClient.Get(ctx, util.BMCKey(util.E2ENamespace), bmc)).To(Succeed())
			if bmc.Spec.Service == nil {
				bmc.Spec.Service = &bmcv1.BMCServiceSpec{}
			}
			bmc.Spec.Service.Labels = map[string]string{"custom-label": "foo"}
			bmc.Spec.Service.Annotations = map[string]string{"custom-annotation": "bar"}
			Expect(k8sClient.Update(ctx, bmc)).To(Succeed())

			By("verifying the Service is patched in-place (same UID) with the new labels and annotations")
			Eventually(func() bool {
				var svc corev1.Service
				if err := k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &svc); err != nil {
					return false
				}
				return svc.UID == svcBefore.UID &&
					svc.Labels["custom-label"] == "foo" &&
					svc.Annotations["custom-annotation"] == "bar"
			}, timeout, interval).Should(BeTrue(), "Service should be patched in-place with the new labels/annotations, without being deleted")

			By("verifying the base labels required for selection are preserved")
			var svcAfterAdd corev1.Service
			Expect(k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &svcAfterAdd)).To(Succeed())
			Expect(svcAfterAdd.Labels).To(HaveKeyWithValue(bmcv1.VirtualMachineBMCNameLabel, util.E2EBMCName))

			By("changing the label/annotation values again")
			bmc = &bmcv1.VirtualMachineBMC{}
			Expect(k8sClient.Get(ctx, util.BMCKey(util.E2ENamespace), bmc)).To(Succeed())
			bmc.Spec.Service.Labels = map[string]string{"custom-label": "baz"}
			bmc.Spec.Service.Annotations = map[string]string{"custom-annotation": "qux"}
			Expect(k8sClient.Update(ctx, bmc)).To(Succeed())

			By("verifying the Service reflects the updated values, still without a UID change")
			Eventually(func() bool {
				var svc corev1.Service
				if err := k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &svc); err != nil {
					return false
				}
				return svc.UID == svcBefore.UID &&
					svc.Labels["custom-label"] == "baz" &&
					svc.Annotations["custom-annotation"] == "qux"
			}, timeout, interval).Should(BeTrue(), "Service labels/annotations should be patched in-place again on subsequent changes")
		})
	})

	Context("when the Service is deleted out-of-band while its type stays the same", Ordered, func() {
		It("should refresh the reported ClusterIP once the owning controller recreates the Service", func() {
			By("recording the current Service and BMC status before deletion")
			var svcBefore corev1.Service
			Expect(k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &svcBefore)).To(Succeed())
			Expect(svcBefore.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))

			var bmcBefore bmcv1.VirtualMachineBMC
			Expect(k8sClient.Get(ctx, util.BMCKey(util.E2ENamespace), &bmcBefore)).To(Succeed())
			Expect(bmcBefore.Status.ClusterIP).To(Equal(svcBefore.Spec.ClusterIP))

			By("deleting the Service directly, simulating an out-of-band deletion")
			Expect(k8sClient.Delete(ctx, &svcBefore)).To(Succeed())

			By("verifying the owning controller automatically recreates the Service with a new UID")
			Eventually(func() bool {
				var svc corev1.Service
				if err := k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &svc); err != nil {
					return false
				}
				return svc.UID != svcBefore.UID && svc.Spec.ClusterIP != ""
			}, timeout, interval).Should(BeTrue(), "Service should be automatically recreated by the owning controller, without any manual action")

			By("verifying the VirtualMachineBMC status tracks the recreated Service's ClusterIP, not the stale one")
			Eventually(func() bool {
				var svc corev1.Service
				if err := k8sClient.Get(ctx, util.AgentPodKey(util.E2ENamespace), &svc); err != nil {
					return false
				}
				var bmc bmcv1.VirtualMachineBMC
				if err := k8sClient.Get(ctx, util.BMCKey(util.E2ENamespace), &bmc); err != nil {
					return false
				}
				return bmc.Status.ClusterIP == svc.Spec.ClusterIP
			}, timeout, interval).Should(BeTrue(), "status.clusterIP should track the current Service's ClusterIP even when the Ready condition's message stays unchanged across the recreation")
		})
	})
})
