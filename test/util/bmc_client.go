package util

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
)

const (
	RedfishClientPodName = "redfish-client"
	IPMIToolPodName      = "ipmitool"
	CurlImage            = "curlimages/curl:latest"
	IPMIToolImage        = "kubevirtbmc/ipmitool:latest" // ipmitool v1.8.19
	helperPodTimeout     = 180 * time.Second
	helperPodInterval    = 250 * time.Millisecond
	sleepDuration        = "999999999"
)

type ExecOptions struct {
	Namespace     string
	PodName       string
	ContainerName string
	Command       []string
}

func ExecInPod(ctx context.Context, cfg *rest.Config, clientset *kubernetes.Clientset, opts ExecOptions) (stdout, stderr string, err error) {
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

func CreateRedfishClientPod(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	return ensureHelperPod(ctx, clientset, namespace, RedfishClientPodName, "curl", CurlImage)
}

func CreateIPMIToolPod(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	return ensureHelperPod(ctx, clientset, namespace, IPMIToolPodName, "ipmitool", IPMIToolImage)
}

// ensureHelperPod waits until a non-terminating pod with the given name is
// Running, creating it when absent.
func ensureHelperPod(ctx context.Context, clientset *kubernetes.Clientset, namespace, podName, containerName, image string) error {
	newPod := func() *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: namespace},
			Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				Containers: []corev1.Container{{
					Name:    containerName,
					Image:   image,
					Command: []string{"sleep", sleepDuration},
				}},
			},
		}
	}
	// Create eagerly so a hard failure (RBAC, invalid spec) surfaces now
	// instead of after the poll timeout. AlreadyExists falls through to the
	// poll: a leftover pod from a previous suite may still be terminating —
	// its phase stays Running while its containers die, and exec into it
	// fails with "container not found" — so a pod with a deletion timestamp
	// is waited out and recreated rather than reused.
	if _, err := clientset.CoreV1().Pods(namespace).Create(ctx, newPod(), metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create %s pod: %w", podName, err)
	}
	var lastCreateErr error
	Eventually(func() bool {
		p, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			_, lastCreateErr = clientset.CoreV1().Pods(namespace).Create(ctx, newPod(), metav1.CreateOptions{})
			return false
		case err != nil, p.DeletionTimestamp != nil:
			return false
		default:
			return p.Status.Phase == corev1.PodRunning
		}
	}, helperPodTimeout, helperPodInterval).Should(BeTrue(), "%s pod should reach Running (last create error: %v)", podName, lastCreateErr)
	return nil
}

func RunCurlInCluster(ctx context.Context, cfg *rest.Config, namespace string, args ...string) (stdout, stderr string, err error) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "", "", fmt.Errorf("building clientset: %w", err)
	}
	if err := CreateRedfishClientPod(ctx, clientset, namespace); err != nil {
		return "", "", err
	}
	cmd := append([]string{"curl"}, args...)
	return ExecInPod(ctx, cfg, clientset, ExecOptions{
		Namespace:     namespace,
		PodName:       RedfishClientPodName,
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

func RunCurlRedfish(ctx context.Context, cfg *rest.Config, namespace string, r RedfishRequest) (string, error) {
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

	out, _, err := RunCurlInCluster(ctx, cfg, namespace, args...)
	return out, err
}

func CreateRedfishSession(ctx context.Context, cfg *rest.Config, namespace, baseURL, username, password string) (token string, err error) {
	body := fmt.Sprintf(`{"UserName":"%s","Password":"%s"}`, username, password)
	out, err := RunCurlRedfish(ctx, cfg, namespace, RedfishRequest{
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
			return strings.TrimSpace(line[idx+1:]), nil
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

func BuildIPMICommand(r IPMIRequest) []string {
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

func RunIPMIInCluster(ctx context.Context, cfg *rest.Config, namespace string, r IPMIRequest) (stdout, stderr string, err error) {
	stdout, stderr, _, err = RunIPMIInClusterTimed(ctx, cfg, namespace, r)
	return stdout, stderr, err
}

func RunIPMIInClusterTimed(ctx context.Context, cfg *rest.Config, namespace string, r IPMIRequest) (stdout, stderr string, elapsed time.Duration, err error) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "", "", 0, fmt.Errorf("building clientset: %w", err)
	}
	if err := CreateIPMIToolPod(ctx, clientset, namespace); err != nil {
		return "", "", 0, err
	}
	start := time.Now()
	stdout, stderr, err = ExecInPod(ctx, cfg, clientset, ExecOptions{
		Namespace:     namespace,
		PodName:       IPMIToolPodName,
		ContainerName: "ipmitool",
		Command:       BuildIPMICommand(r),
	})
	return stdout, stderr, time.Since(start), err
}
