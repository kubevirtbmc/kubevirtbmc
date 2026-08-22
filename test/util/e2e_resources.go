package util

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
)

const (
	E2EVMName                  = "testvm"
	E2ESecretName              = "bmc-credentials-secret"
	E2EBMCName                 = "test-bmc"
	E2ENamespace               = "default"
	E2EAgentDeploymentName     = E2EVMName + "-virtbmc"
	E2EWebhookServiceName      = "kubevirtbmc-webhook-service"
	E2EWebhookServiceNamespace = "kubevirtbmc-system"
	WebhookRegistrationDelay   = 10 * time.Second
)

func AgentDeploymentKey(namespace string) types.NamespacedName {
	return types.NamespacedName{Name: E2EAgentDeploymentName, Namespace: namespace}
}

func AgentServiceKey(namespace string) types.NamespacedName {
	return types.NamespacedName{Name: E2EAgentDeploymentName, Namespace: namespace}
}

func AgentPodKey(namespace string) types.NamespacedName {
	return types.NamespacedName{Name: E2EAgentDeploymentName, Namespace: namespace}
}

var agentPodLabels = client.MatchingLabels{
	bmcv1.VirtualMachineBMCNameLabel: E2EBMCName,
	bmcv1.VMNameLabel:                E2EVMName,
}

// AgentPod errors unless exactly one Pod matches, since Deployment-managed Pods have a generated name.
func AgentPod(ctx context.Context, k8sClient client.Client, namespace string) (*corev1.Pod, error) {
	var podList corev1.PodList
	if err := k8sClient.List(ctx, &podList, client.InNamespace(namespace), agentPodLabels); err != nil {
		return nil, err
	}
	if len(podList.Items) != 1 {
		return nil, apierrors.NewNotFound(corev1.Resource("pods"), E2EAgentDeploymentName)
	}
	return &podList.Items[0], nil
}

func BMCKey(namespace string) types.NamespacedName {
	return types.NamespacedName{Name: E2EBMCName, Namespace: namespace}
}

func HasBMCCondition(ctx context.Context, k8sClient client.Client, namespace, condType string, status metav1.ConditionStatus, reason string) func() bool {
	return func() bool {
		var bmc bmcv1.VirtualMachineBMC
		if err := k8sClient.Get(ctx, BMCKey(namespace), &bmc); err != nil {
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

func DeploymentReady(ctx context.Context, k8sClient client.Client, namespace string) func() bool {
	return func() bool {
		deployment := &appsv1.Deployment{}
		if err := k8sClient.Get(ctx, AgentDeploymentKey(namespace), deployment); err != nil {
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
	}
}

func DeploymentExists(ctx context.Context, k8sClient client.Client, namespace string) func() bool {
	return func() bool {
		deployment := &appsv1.Deployment{}
		return k8sClient.Get(ctx, AgentDeploymentKey(namespace), deployment) == nil
	}
}

func DeploymentNotFound(ctx context.Context, k8sClient client.Client, namespace string) func() bool {
	return func() bool {
		deployment := &appsv1.Deployment{}
		return apierrors.IsNotFound(k8sClient.Get(ctx, AgentDeploymentKey(namespace), deployment))
	}
}

func ServiceNotFound(ctx context.Context, k8sClient client.Client, namespace string) func() bool {
	return func() bool {
		svc := &corev1.Service{}
		return apierrors.IsNotFound(k8sClient.Get(ctx, AgentServiceKey(namespace), svc))
	}
}

func PodRunningAndReadyWithNewUID(ctx context.Context, k8sClient client.Client, namespace string, oldUID types.UID) func() bool {
	return func() bool {
		pod, err := AgentPod(ctx, k8sClient, namespace)
		if err != nil {
			return false
		}
		if pod.UID == oldUID {
			return false
		}
		if pod.Status.Phase != corev1.PodRunning {
			return false
		}
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	}
}

func VMIPhase(ctx context.Context, k8sClient client.Client, name, namespace string, phase kubevirtv1.VirtualMachineInstancePhase) func() bool {
	return func() bool {
		vmi := &kubevirtv1.VirtualMachineInstance{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, vmi); err != nil {
			return false
		}
		return vmi.Status.Phase == phase
	}
}

func VMIRunning(ctx context.Context, k8sClient client.Client, name, namespace string) func() bool {
	return VMIPhase(ctx, k8sClient, name, namespace, kubevirtv1.Running)
}

func VMNotFound(ctx context.Context, k8sClient client.Client, name, namespace string) func() bool {
	return func() bool {
		return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &kubevirtv1.VirtualMachine{}))
	}
}

// WaitForWebhookServiceReady blocks until the webhook service has at least one ready pod or the timeout is reached.
func WaitForWebhookServiceReady(ctx context.Context, k8sClient client.Client, timeout, interval time.Duration) {
	webhookKey := types.NamespacedName{Name: E2EWebhookServiceName, Namespace: E2EWebhookServiceNamespace}
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
}

// WaitForWebhookEndpointSlicesReady blocks until the webhook Service has at least one ready endpoint in EndpointSlices.
// This avoids a race where Pods are Ready but Service endpoints haven't been programmed yet.
func WaitForWebhookEndpointSlicesReady(ctx context.Context, k8sClient client.Client, timeout, interval time.Duration) {
	Eventually(func() bool {
		var slices discoveryv1.EndpointSliceList
		if err := k8sClient.List(ctx, &slices, &client.ListOptions{
			Namespace:     E2EWebhookServiceNamespace,
			LabelSelector: labels.SelectorFromSet(labels.Set{"kubernetes.io/service-name": E2EWebhookServiceName}),
		}); err != nil {
			return false
		}
		for _, s := range slices.Items {
			for _, ep := range s.Endpoints {
				if len(ep.Addresses) == 0 {
					continue
				}
				// Treat nil as "unknown"; accept Ready==true or nil to avoid false negatives during propagation.
				if ep.Conditions.Ready == nil || *ep.Conditions.Ready {
					return true
				}
			}
		}
		return false
	}, timeout, interval).Should(BeTrue(), "webhook service should have at least one ready endpoint (EndpointSlice)")
}

func WaitForControllerManagerReady(ctx context.Context, k8sClient client.Client, timeout, interval time.Duration) {
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
}

func EnsureTestVMSecretBMC(ctx context.Context, k8sClient client.Client, namespace string, timeout, interval time.Duration) error {
	vm := E2EVM(namespace)
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(vm), &kubevirtv1.VirtualMachine{}); err != nil {
		if apierrors.IsNotFound(err) {
			if err := k8sClient.Create(ctx, vm); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	secret := E2ESecret(namespace)
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(secret), &corev1.Secret{}); err != nil {
		if apierrors.IsNotFound(err) {
			if err := k8sClient.Create(ctx, secret); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	bmc := E2EBMC(namespace)
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(bmc), &bmcv1.VirtualMachineBMC{}); err != nil {
		if apierrors.IsNotFound(err) {
			if err := k8sClient.Create(ctx, bmc); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	By("waiting for VMI to reach Running and agent Deployment to become ready")
	Eventually(VMIRunning(ctx, k8sClient, E2EVMName, namespace), timeout, interval).Should(BeTrue(), "VMI %s/%s should reach Running", namespace, E2EVMName)
	Eventually(DeploymentReady(ctx, k8sClient, namespace), timeout, interval).Should(BeTrue(), "agent Deployment %s/%s should become ready", namespace, E2EAgentDeploymentName)
	return nil
}

func E2EVM(namespace string) *kubevirtv1.VirtualMachine {
	runStrategy := kubevirtv1.RunStrategyAlways
	guestMemory := resource.MustParse("256Mi")
	terminationGracePeriodSeconds := int64(3)
	return &kubevirtv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Name: E2EVMName, Namespace: namespace},
		Spec: kubevirtv1.VirtualMachineSpec{
			RunStrategy: &runStrategy,
			Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
				Spec: kubevirtv1.VirtualMachineInstanceSpec{
					TerminationGracePeriodSeconds: &terminationGracePeriodSeconds,
					Domain: kubevirtv1.DomainSpec{
						Memory: &kubevirtv1.Memory{Guest: &guestMemory},
						Resources: kubevirtv1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
						},
						Devices: kubevirtv1.Devices{
							Disks: []kubevirtv1.Disk{
								{Name: "containerdisk", DiskDevice: kubevirtv1.DiskDevice{Disk: &kubevirtv1.DiskTarget{Bus: "virtio"}}},
								{Name: "cdrom", DiskDevice: kubevirtv1.DiskDevice{CDRom: &kubevirtv1.CDRomTarget{Bus: "sata"}}},
							},
							Interfaces: []kubevirtv1.Interface{
								{Name: "default", InterfaceBindingMethod: kubevirtv1.InterfaceBindingMethod{Masquerade: &kubevirtv1.InterfaceMasquerade{}}},
							},
							Rng: &kubevirtv1.Rng{},
						},
						RebootPolicy: ptr.To(kubevirtv1.RebootPolicyTerminate),
					},
					Volumes: []kubevirtv1.Volume{
						{Name: "containerdisk", VolumeSource: kubevirtv1.VolumeSource{
							ContainerDisk: &kubevirtv1.ContainerDiskSource{
								Image:           "quay.io/kubevirt/fedora-with-test-tooling-container-disk:devel",
								ImagePullPolicy: corev1.PullIfNotPresent,
							},
						}},
						{Name: "cdrom", VolumeSource: kubevirtv1.VolumeSource{
							EmptyDisk: &kubevirtv1.EmptyDiskSource{Capacity: resource.MustParse("1Gi")},
						}},
					},
					Networks: []kubevirtv1.Network{
						{Name: "default", NetworkSource: kubevirtv1.NetworkSource{Pod: &kubevirtv1.PodNetwork{}}},
					},
				},
			},
		},
	}
}

func E2ESecret(namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: E2ESecretName, Namespace: namespace},
		Data: map[string][]byte{
			"username": []byte("admin"),
			"password": []byte("password"),
		},
	}
}

func E2EBMC(namespace string) *bmcv1.VirtualMachineBMC {
	return &bmcv1.VirtualMachineBMC{
		ObjectMeta: metav1.ObjectMeta{Name: E2EBMCName, Namespace: namespace},
		Spec: bmcv1.VirtualMachineBMCSpec{
			VirtualMachineRef: &corev1.LocalObjectReference{Name: E2EVMName},
			AuthSecretRef:     &corev1.LocalObjectReference{Name: E2ESecretName},
		},
	}
}
