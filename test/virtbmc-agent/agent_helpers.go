package virtbmcagent

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	kubescheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
)

const (
	agentNamespace       = "default"
	agentVMName          = "testvm"
	agentBMCName         = "test-bmc"
	agentSecretName      = "bmc-credentials-secret"
	curlImage            = "curlimages/curl:latest"
	ipmitoolImage        = "mikeynap/ipmitool:latest"
	agentTestTimeout     = 60 * time.Second
	agentTestInterval    = 250 * time.Millisecond
	agentPodName         = "testvm-virtbmc"
	vmPowerStatusTimeout = 120 * time.Second
	helperPodTimeout     = 180 * time.Second

	redfishClientPodName = "redfish-client"
	ipmitoolPodName      = "ipmitool"
	sleepDuration        = "999999999"
)

type agentTestEnv struct {
	VM             *kubevirtv1.VirtualMachine
	Secret         *corev1.Secret
	BMC            *bmcv1.VirtualMachineBMC
	Namespace      string
	RedfishBaseURL string
	ServiceHost    string
	Username       string
	Password       string
}

func redfishBaseURL(vmName, namespace string) string {
	return fmt.Sprintf("http://%s/redfish/v1", serviceHostname(vmName, namespace))
}

func serviceHostname(vmName, namespace string) string {
	return fmt.Sprintf("%s-virtbmc.%s.svc.cluster.local", vmName, namespace)
}

func ensureAgentTestEnv(ctx context.Context, namespace string, k8sClient client.Client) (*agentTestEnv, error) {
	env := &agentTestEnv{
		Namespace:      namespace,
		ServiceHost:    serviceHostname(agentVMName, namespace),
		RedfishBaseURL: redfishBaseURL(agentVMName, namespace),
	}

	if err := env.ensureVMExists(ctx, k8sClient, namespace); err != nil {
		return nil, err
	}

	if err := env.ensureSecretExists(ctx, k8sClient, namespace); err != nil {
		return nil, err
	}

	if err := env.ensureBMCExists(ctx, k8sClient, namespace); err != nil {
		return nil, err
	}

	waitForAgentPodReady(ctx, k8sClient, namespace, agentPodName)

	return env, nil
}

func (e *agentTestEnv) ensureVMExists(ctx context.Context, k8sClient client.Client, namespace string) error {
	vm := &kubevirtv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentVMName,
			Namespace: namespace,
		},
	}
	return k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentVMName}, vm)
}

func isVMIPhase(ctx context.Context, k8sClient client.Client, namespace, vmName string, phase kubevirtv1.VirtualMachineInstancePhase) bool {
	vmi := &kubevirtv1.VirtualMachineInstance{}
	err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: vmName}, vmi)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false
		}
		return false
	}
	return vmi.Status.Phase == phase
}

func isVMIDeleted(ctx context.Context, k8sClient client.Client, namespace, vmName string) bool {
	err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: vmName}, &kubevirtv1.VirtualMachineInstance{})
	return apierrors.IsNotFound(err)
}

func waitForVMIDeleted(ctx context.Context, k8sClient client.Client, namespace string) {
	Eventually(func() bool {
		return isVMIDeleted(ctx, k8sClient, namespace, agentVMName)
	}, vmPowerStatusTimeout, agentTestInterval).Should(BeTrue(),
		"VMI %s/%s should be deleted (power off)", namespace, agentVMName)
}

func waitForVMIRunning(ctx context.Context, k8sClient client.Client, namespace string) {
	Eventually(func() bool {
		return isVMIPhase(ctx, k8sClient, namespace, agentVMName, kubevirtv1.Running)
	}, vmPowerStatusTimeout, agentTestInterval).Should(BeTrue(),
		"VMI %s/%s should reach Running phase", namespace, agentVMName)
}

func waitForVMIPowerCycle(ctx context.Context, k8sClient client.Client, namespace string) {
	orig := &kubevirtv1.VirtualMachineInstance{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentVMName}, orig)).To(Succeed(),
		"VMI must exist before power cycle")
	origUID := orig.UID

	Eventually(func() bool {
		curr := &kubevirtv1.VirtualMachineInstance{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentVMName}, curr); err != nil {
			return false
		}
		if curr.Status.Phase != kubevirtv1.Running {
			return false
		}
		return curr.UID != origUID
	}, vmPowerStatusTimeout, agentTestInterval).Should(BeTrue(),
		"VMI %s/%s should be recreated with a new UID and reach Running phase", namespace, agentVMName)
}

// verifyVMBootOrder fetches the test VM and checks that bootOrder values on
// disks and interfaces match the expected maps. It retries until the timeout
// because the update goes through agent → resourcemanager → KubeVirt API.
func verifyVMBootOrder(ctx context.Context, k8sClient client.Client, namespace string, expectedDisks, expectedIfaces map[int]uint) {
	Eventually(func() bool {
		vm := &kubevirtv1.VirtualMachine{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentVMName}, vm); err != nil {
			return false
		}
		if vm.Spec.Template == nil {
			return false
		}

		disks := vm.Spec.Template.Spec.Domain.Devices.Disks
		ifaces := vm.Spec.Template.Spec.Domain.Devices.Interfaces

		for idx, expectedOrder := range expectedDisks {
			if idx >= len(disks) || disks[idx].BootOrder == nil || *disks[idx].BootOrder != expectedOrder {
				return false
			}
		}
		for idx, expectedOrder := range expectedIfaces {
			if idx >= len(ifaces) || ifaces[idx].BootOrder == nil || *ifaces[idx].BootOrder != expectedOrder {
				return false
			}
		}
		return true
	}, vmPowerStatusTimeout, agentTestInterval).Should(BeTrue(),
		"VM %s/%s boot order should match: disks=%v ifaces=%v",
		namespace, agentVMName, expectedDisks, expectedIfaces)
}

func (e *agentTestEnv) ensureSecretExists(ctx context.Context, k8sClient client.Client, namespace string) error {
	secret := &corev1.Secret{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentSecretName}, secret); err != nil {
		return err
	}
	e.Secret = secret
	if u, ok := secret.Data["username"]; ok {
		e.Username = string(u)
	}
	if p, ok := secret.Data["password"]; ok {
		e.Password = string(p)
	}
	return nil
}

func (e *agentTestEnv) ensureBMCExists(ctx context.Context, k8sClient client.Client, namespace string) error {
	bmc := &bmcv1.VirtualMachineBMC{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentBMCName,
			Namespace: namespace,
		},
	}
	return k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentBMCName}, bmc)
}

func waitForAgentPodReady(ctx context.Context, k8sClient client.Client, namespace, podName string) {
	Eventually(func() bool {
		var pod corev1.Pod
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: podName}, &pod); err != nil {
			return false
		}
		for _, c := range pod.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	}, agentTestTimeout, agentTestInterval).Should(BeTrue(), "agent pod %q should become ready", podName)
}

func CreateRedfishClientPod(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: redfishClientPodName, Namespace: namespace},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:    "curl",
					Image:   curlImage,
					Command: []string{"sleep", sleepDuration},
				},
			},
		},
	}
	_, err := clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create redfish-client pod: %w", err)
	}
	Eventually(func() bool {
		p, getErr := clientset.CoreV1().Pods(namespace).Get(ctx, redfishClientPodName, metav1.GetOptions{})
		if getErr != nil {
			return false
		}
		return p.Status.Phase == corev1.PodRunning
	}, helperPodTimeout, agentTestInterval).Should(BeTrue(), "redfish-client pod should reach Running")
	return nil
}

func CreateIPMIToolPod(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: ipmitoolPodName, Namespace: namespace},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:    "ipmitool",
					Image:   ipmitoolImage,
					Command: []string{"sleep", sleepDuration},
				},
			},
		},
	}
	_, err := clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create ipmitool pod: %w", err)
	}
	Eventually(func() bool {
		p, getErr := clientset.CoreV1().Pods(namespace).Get(ctx, ipmitoolPodName, metav1.GetOptions{})
		if getErr != nil {
			return false
		}
		return p.Status.Phase == corev1.PodRunning
	}, helperPodTimeout, agentTestInterval).Should(BeTrue(), "ipmitool pod should reach Running")
	return nil
}

type execOptions struct {
	Namespace     string
	PodName       string
	ContainerName string
	Command       []string
}

func execInPod(ctx context.Context, cfg *rest.Config, clientset *kubernetes.Clientset, opts execOptions) (stdout, stderr string, err error) {
	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(opts.Namespace).
		Name(opts.PodName).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: opts.ContainerName,
			Command:   opts.Command,
			Stdout:    true,
			Stderr:    true,
		}, kubescheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
	if err != nil {
		return "", "", fmt.Errorf("creating SPDY executor: %w", err)
	}

	var outBuf, errBuf bytes.Buffer
	if err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &outBuf,
		Stderr: &errBuf,
	}); err != nil {
		return outBuf.String(), errBuf.String(), fmt.Errorf("exec stream: %w", err)
	}

	return outBuf.String(), errBuf.String(), nil
}

func runCurlInCluster(ctx context.Context, cfg *rest.Config, namespace string, args ...string) (stdout, stderr string, err error) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "", "", fmt.Errorf("building clientset: %w", err)
	}
	if err := CreateRedfishClientPod(ctx, clientset, namespace); err != nil {
		return "", "", err
	}
	cmd := append([]string{"curl"}, args...)
	return execInPod(ctx, cfg, clientset, execOptions{
		Namespace:     namespace,
		PodName:       redfishClientPodName,
		ContainerName: "curl",
		Command:       cmd,
	})
}

type RedfishRequest struct {
	BaseURL    string
	Method     string
	Path       string
	Body       string
	Username   string
	Password   string
	XAuthToken string
}

func runCurlRedfish(ctx context.Context, cfg *rest.Config, namespace string, r RedfishRequest) (string, error) {
	url := r.BaseURL
	if r.Path != "" {
		url = strings.TrimSuffix(r.BaseURL, "/") + r.Path
	}
	args := []string{"--connect-timeout", "5", "--max-time", "15", "-i", "-L", "-X", r.Method}
	if r.XAuthToken != "" {
		args = append(args, "-H", "X-Auth-Token: "+r.XAuthToken)
	} else if r.Username != "" && r.Password != "" {
		args = append(args, "-u", r.Username+":"+r.Password)
	}
	if r.Body != "" {
		args = append(args, "-H", "Content-Type: application/json", "-d", r.Body)
	}
	args = append(args, url)

	out, _, err := runCurlInCluster(ctx, cfg, namespace, args...)
	return out, err
}

func CreateRedfishSession(ctx context.Context, cfg *rest.Config, namespace, baseURL, username, password string) (token string, err error) {
	body := fmt.Sprintf(`{"UserName":"%s","Password":"%s"}`, username, password)
	out, err := runCurlRedfish(ctx, cfg, namespace, RedfishRequest{
		BaseURL:  baseURL,
		Method:   "POST",
		Path:     "/SessionService/Sessions",
		Body:     body,
		Username: username,
		Password: password,
	})
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if idx := strings.Index(line, ":"); idx >= 0 && strings.EqualFold(strings.TrimSpace(line[:idx]), "X-Auth-Token") {
			token = strings.TrimSpace(line[idx+1:])
			return token, nil
		}
	}
	return "", fmt.Errorf("X-Auth-Token not found in session response")
}

type IPMIRequest struct {
	ServiceHost string
	Username    string
	Password    string
	Interface   string // "lan" or "lanplus"; defaults to "lanplus"
	RetryCount  int    // when > 0, passes -R to ipmitool (retry count)
	Args        []string
}

func buildIPMICommand(r IPMIRequest) []string {
	iface := r.Interface
	if iface == "" {
		iface = "lanplus"
	}
	cmd := []string{"ipmitool", "-I", iface, "-U", r.Username, "-P", r.Password, "-H", r.ServiceHost}
	if r.RetryCount > 0 {
		cmd = append(cmd, "-R", strconv.Itoa(r.RetryCount))
	}
	return append(cmd, r.Args...)
}

func runIPMIInCluster(ctx context.Context, cfg *rest.Config, namespace string, r IPMIRequest) (stdout, stderr string, err error) {
	stdout, stderr, _, err = runIPMIInClusterTimed(ctx, cfg, namespace, r)
	return stdout, stderr, err
}

func runIPMIInClusterTimed(ctx context.Context, cfg *rest.Config, namespace string, r IPMIRequest) (stdout, stderr string, elapsed time.Duration, err error) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "", "", 0, fmt.Errorf("building clientset: %w", err)
	}
	if err := CreateIPMIToolPod(ctx, clientset, namespace); err != nil {
		return "", "", 0, err
	}

	start := time.Now()
	stdout, stderr, err = execInPod(ctx, cfg, clientset, execOptions{
		Namespace:     namespace,
		PodName:       ipmitoolPodName,
		ContainerName: "ipmitool",
		Command:       buildIPMICommand(r),
	})
	return stdout, stderr, time.Since(start), err
}

func verifyDataVolumeExists(ctx context.Context, k8sClient client.Client, namespace, name string) {
	Eventually(func() error {
		dv := &cdiv1.DataVolume{}
		return k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, dv)
	}, agentTestTimeout, agentTestInterval).Should(Succeed(),
		"DataVolume %s/%s should exist", namespace, name)
}

func verifyDataVolumeDeleted(ctx context.Context, k8sClient client.Client, namespace, name string) {
	Eventually(func() bool {
		dv := &cdiv1.DataVolume{}
		err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, dv)
		return apierrors.IsNotFound(err)
	}, agentTestTimeout, agentTestInterval).Should(BeTrue(),
		"DataVolume %s/%s should be deleted", namespace, name)
}

func verifyVMHasDataVolumeVolume(ctx context.Context, k8sClient client.Client, namespace, vmName, dvName string) {
	Eventually(func() bool {
		vm := &kubevirtv1.VirtualMachine{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: vmName}, vm); err != nil {
			return false
		}
		if vm.Spec.Template == nil {
			return false
		}
		for _, v := range vm.Spec.Template.Spec.Volumes {
			if v.DataVolume != nil && v.DataVolume.Name == dvName {
				return true
			}
		}
		return false
	}, agentTestTimeout, agentTestInterval).Should(BeTrue(),
		"VM %s/%s should have a volume with DataVolume source %q", namespace, vmName, dvName)
}

func verifyVMHasNoDataVolumeVolume(ctx context.Context, k8sClient client.Client, namespace, vmName string) {
	Eventually(func() bool {
		vm := &kubevirtv1.VirtualMachine{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: vmName}, vm); err != nil {
			return false
		}
		if vm.Spec.Template == nil {
			return false
		}
		for _, v := range vm.Spec.Template.Spec.Volumes {
			if v.DataVolume != nil {
				return false
			}
		}
		return true
	}, agentTestTimeout, agentTestInterval).Should(BeTrue(),
		"VM %s/%s should have no DataVolume volumes", namespace, vmName)
}
