package virtbmcagent

import (
	"context"
	"os"
	"strings"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
	testutil "kubevirt.io/kubevirtbmc/test/util"
)

// Standalone e2e runs the same specs as the controller-managed suite, but the
// agent is a plain Deployment created by the test itself: no CRD, no
// controller, no cert-manager. Divergences from the managed mode:
//
//   - Agent config comes from flags/env (--standalone, --enable-ipmi,
//     --state-file, BMC_USERNAME/BMC_PASSWORD), so the "IPMI toggle" and
//     "storageClassName override" contexts — both CR-driven — are skipped.
//   - Boot override state lives in the state file, so assertions read it back
//     through the IPMI protocol (GetBootFlags is store-backed) instead of the
//     CR status.
//   - resetBootState clears the override through the Redfish protocol (PATCH
//     Boot.Enabled=Disabled) instead of writing the CR status directly.
//   - The state file lives on a PVC so the "survive agent pod restart" spec
//     exercises the file store's durability.

var standaloneMode = os.Getenv("AGENT_STANDALONE") == envTrueValue

const (
	standaloneUsername   = "admin"
	standalonePassword   = "password"
	standaloneSAName     = agentDeploymentName
	standaloneStatePVC   = agentDeploymentName + "-state"
	standaloneStateMount = "/var/lib/virtbmc"
)

var standalonePodLabels = map[string]string{
	bmcv1.VirtualMachineBMCNameLabel: agentBMCName,
	bmcv1.VMNameLabel:                agentVMName,
}

// ensureStandaloneAgent creates everything the standalone agent needs, in the
// same shape the controller would produce (Deployment/Service names, pod
// labels) so the shared specs and util helpers work unchanged.
func ensureStandaloneAgent(ctx context.Context, k8sClient client.Client, namespace string) error {
	objs := []client.Object{
		standaloneServiceAccount(namespace),
		standaloneRole(namespace),
		standaloneRoleBinding(namespace),
		standaloneStateVolumeClaim(namespace),
		standaloneDeployment(namespace),
		standaloneService(namespace),
	}
	for _, obj := range objs {
		if err := k8sClient.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	waitForAgentDeploymentReady(ctx, k8sClient, namespace, agentDeploymentName)
	return nil
}

func deleteStandaloneAgent(ctx context.Context, k8sClient client.Client, namespace string) {
	names := map[client.Object]string{
		&appsv1.Deployment{}:            agentDeploymentName,
		&corev1.Service{}:               agentDeploymentName,
		&corev1.ServiceAccount{}:        standaloneSAName,
		&rbacv1.Role{}:                  standaloneSAName,
		&rbacv1.RoleBinding{}:           standaloneSAName,
		&corev1.PersistentVolumeClaim{}: standaloneStatePVC,
	}
	for obj, name := range names {
		obj.SetName(name)
		obj.SetNamespace(namespace)
		if err := k8sClient.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			Expect(err).NotTo(HaveOccurred())
		}
	}
}

func standaloneServiceAccount(namespace string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: standaloneSAName, Namespace: namespace},
	}
}

// standaloneRole mirrors the RBAC documented for standalone mode: KubeVirt and
// CDI permissions only, nothing from bmc.kubevirt.io.
func standaloneRole(namespace string) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: standaloneSAName, Namespace: namespace},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"kubevirt.io"},
				Resources: []string{"virtualmachines"},
				Verbs:     []string{"get", "update", "patch"},
			},
			{
				APIGroups: []string{"subresources.kubevirt.io"},
				Resources: []string{"virtualmachines/start", "virtualmachines/stop", "virtualmachines/restart"},
				Verbs:     []string{"update"},
			},
			{
				APIGroups: []string{"kubevirt.io"},
				Resources: []string{"virtualmachineinstances"},
				Verbs:     []string{"get"},
			},
			{
				APIGroups: []string{"cdi.kubevirt.io"},
				Resources: []string{"datavolumes"},
				Verbs:     []string{"create", "delete"},
			},
		},
	}
}

func standaloneRoleBinding(namespace string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: standaloneSAName, Namespace: namespace},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     standaloneSAName,
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      standaloneSAName,
			Namespace: namespace,
		}},
	}
}

func standaloneStateVolumeClaim(namespace string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: standaloneStatePVC, Namespace: namespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Mi")},
			},
		},
	}
}

func standaloneDeployment(namespace string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentDeploymentName,
			Namespace: namespace,
			Labels:    standalonePodLabels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			// Recreate avoids two pods fighting over the RWO state volume.
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{MatchLabels: standalonePodLabels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: standalonePodLabels},
				Spec: corev1.PodSpec{
					ServiceAccountName: standaloneSAName,
					Containers: []corev1.Container{{
						Name:  "virtbmc",
						Image: agentImage,
						Args: []string{
							"--address", "0.0.0.0",
							"--redfish-port", "10080",
							"--enable-ipmi", "--ipmi-port", "10623",
							"--standalone",
							"--state-file", standaloneStateMount + "/state.json",
							namespace, agentVMName,
						},
						Env: []corev1.EnvVar{
							{Name: "BMC_USERNAME", Value: standaloneUsername},
							{Name: "BMC_PASSWORD", Value: standalonePassword},
						},
						Ports: []corev1.ContainerPort{
							{Name: "redfish", ContainerPort: 10080, Protocol: corev1.ProtocolTCP},
							{Name: "ipmi", ContainerPort: 10623, Protocol: corev1.ProtocolUDP},
						},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/redfish/v1",
									Port: intstr.FromString("redfish"),
								},
							},
							InitialDelaySeconds: 2,
							PeriodSeconds:       10,
						},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "state",
							MountPath: standaloneStateMount,
						}},
					}},
					Volumes: []corev1.Volume{{
						Name: "state",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: standaloneStatePVC,
							},
						},
					}},
				},
			},
		},
	}
}

func standaloneService(namespace string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentDeploymentName,
			Namespace: namespace,
			Labels:    standalonePodLabels,
		},
		Spec: corev1.ServiceSpec{
			Selector: standalonePodLabels,
			Ports: []corev1.ServicePort{
				{Name: "redfish", Port: 80, TargetPort: intstr.FromString("redfish"), Protocol: corev1.ProtocolTCP},
				{Name: "ipmi", Port: 623, TargetPort: intstr.FromString("ipmi"), Protocol: corev1.ProtocolUDP},
			},
		},
	}
}

func standaloneIPMIRequest(namespace string, args ...string) IPMIRequest {
	return IPMIRequest{
		ServiceHost: serviceHostname(agentVMName, namespace),
		Username:    standaloneUsername,
		Password:    standalonePassword,
		Args:        args,
	}
}

// verifyStandaloneBootOverride checks the persisted boot override through
// IPMI: the "Boot Flag Valid" bit of Get System Boot Options is rendered from
// GetBootFlags, which reads the state store on every call — equivalent to the
// CR status assertion used in controller-managed mode. A failing command (no
// bootable devices configured) is treated as "not yet" and retried.
func verifyStandaloneBootOverride(ctx context.Context, namespace string, shouldExist bool) {
	Eventually(func() bool {
		out, _, err := testutil.RunIPMIInCluster(ctx, config, namespace,
			standaloneIPMIRequest(namespace, "chassis", "bootparam", "get", "5"))
		if err != nil {
			return false
		}
		return strings.Contains(out, "Boot Flag Valid") == shouldExist
	}, agentTestTimeout, agentTestInterval).Should(BeTrue(),
		"standalone boot override present should be %v (state file)", shouldExist)
}

// verifyStandaloneBootOverrideMode checks the persistence bit via ipmitool's
// rendering: "only next boot" (oneshot) vs "all future boots" (persistent).
func verifyStandaloneBootOverrideMode(ctx context.Context, namespace string, mode bmcv1.BootOverrideMode) {
	want := "only next boot"
	if mode == bmcv1.BootOverrideModePersistent {
		want = "all future boots"
	}
	Eventually(func() string {
		out, _, err := testutil.RunIPMIInCluster(ctx, config, namespace,
			standaloneIPMIRequest(namespace, "chassis", "bootparam", "get", "5"))
		if err != nil {
			return ""
		}
		if strings.Contains(out, want) {
			return want
		}
		return ""
	}, agentTestTimeout, agentTestInterval).Should(Equal(want),
		"standalone boot override mode should be %s", mode)
}

// clearStandaloneBootOverride clears the override through the Redfish protocol
// (PATCH Boot.BootSourceOverrideEnabled=Disabled with basic auth). It runs
// before the VM bootOrder cleanup in resetBootState: clearing after would let
// ClearBootOverrides restore the backed-up boot orders the cleanup just
// removed.
func clearStandaloneBootOverride(ctx context.Context, namespace string) {
	_, _ = testutil.RunCurlRedfish(ctx, config, namespace, RedfishRequest{
		BaseURL:  redfishBaseURL(agentVMName, namespace),
		Method:   "PATCH",
		Path:     "/Systems/1",
		Body:     `{"Boot":{"BootSourceOverrideEnabled":"Disabled"}}`,
		Username: standaloneUsername,
		Password: standalonePassword,
	})
}
