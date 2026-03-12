package virtbmcagent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
	"kubevirt.io/kubevirtbmc/test/util"
)

var (
	skipKubeVirtInstall           = os.Getenv("KUBEVIRT_INSTALL_SKIP") == "true"
	skipCertManagerInstall        = os.Getenv("CERT_MANAGER_INSTALL_SKIP") == "true"
	isCertManagerAlreadyInstalled = false

	controllerManagerImage = fmt.Sprintf("starbops/virtbmc-controller:%s", func() string {
		if tag := os.Getenv("TAG"); tag != "" {
			return tag
		}
		return "dev"
	}())
	agentImage = fmt.Sprintf("starbops/virtbmc:%s", func() string {
		if tag := os.Getenv("TAG"); tag != "" {
			return tag
		}
		return "dev"
	}())

	config    *rest.Config
	k8sClient client.Client
)

func TestVirtBMCAgent(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting KubeVirtBMC agent E2E test suite\n")
	RunSpecs(t, "VirtBMC Agent E2E Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("loading the built images to the Kind cluster")
	err := util.LoadImageToKindClusterWithName(controllerManagerImage, agentImage)
	Expect(err).ToNot(HaveOccurred())

	By("creating the clientsets")
	var err2 error
	config, err2 = getClientConfig()
	Expect(err2).ToNot(HaveOccurred())
	Expect(kubevirtv1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(bmcv1.AddToScheme(scheme.Scheme)).To(Succeed())
	k8sClient, err2 = client.New(config, client.Options{Scheme: scheme.Scheme})
	Expect(err2).ToNot(HaveOccurred())

	if !skipKubeVirtInstall {
		By("installing KubeVirt (if not already installed)")
		if !util.IsKubeVirtInstalled() {
			err2 = util.InstallKubeVirt()
			Expect(err2).ToNot(HaveOccurred())
		} else {
			_, _ = fmt.Fprintf(GinkgoWriter, "KubeVirt already installed. Skipping...\n")
		}
	}

	if !skipCertManagerInstall {
		By("checking if cert-manager is installed already")
		isCertManagerAlreadyInstalled = util.IsCertManagerCRDsInstalled()
		if !isCertManagerAlreadyInstalled {
			_, _ = fmt.Fprintf(GinkgoWriter, "Installing cert-manager...\n")
			err2 = util.InstallCertManager()
			Expect(err2).ToNot(HaveOccurred())
		} else {
			_, _ = fmt.Fprintf(GinkgoWriter, "cert-manager already installed. Skipping...\n")
		}
	}

	By("deploying the controller-manager")
	cmd := exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", controllerManagerImage))
	_, err2 = util.Run(cmd)
	Expect(err2).ToNot(HaveOccurred())

	ctx := context.Background()
	By("waiting for the controller-manager to be up and running")
	util.WaitForControllerManagerReady(ctx, k8sClient, agentTestTimeout, agentTestInterval)

	By("waiting for the webhook service to be ready")
	util.WaitForWebhookServiceReady(ctx, k8sClient, agentTestTimeout, agentTestInterval)
	By("waiting for webhook endpoints to be ready")
	util.WaitForWebhookEndpointSlicesReady(ctx, k8sClient, agentTestTimeout, agentTestInterval)
	By("waiting for webhook registration to propagate")
	time.Sleep(util.WebhookRegistrationDelay)

	By("ensuring test VM, Secret and VirtualMachineBMC exist and waiting for VMI and agent pod")
	Expect(util.EnsureTestVMSecretBMC(ctx, k8sClient, agentNamespace, agentTestTimeout, agentTestInterval)).To(Succeed())
})

var _ = AfterSuite(func() {
	By("undeploying the controller-manager")
	cmd := exec.Command("make", "undeploy")
	_, _ = util.Run(cmd)

	if !skipCertManagerInstall && !isCertManagerAlreadyInstalled {
		_, _ = fmt.Fprintf(GinkgoWriter, "Uninstalling cert-manager (was installed by this suite)...\n")
		util.UninstallCertManager()
	}

	if k8sClient != nil {
		ctx := context.Background()

		By("deleting test VirtualMachine, Secret, VirtualMachineBMC and helper pods")

		objs := []client.Object{
			&kubevirtv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentVMName,
					Namespace: agentNamespace,
				},
			},
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentSecretName,
					Namespace: agentNamespace,
				},
			},
			&bmcv1.VirtualMachineBMC{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentBMCName,
					Namespace: agentNamespace,
				},
			},
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      ipmitoolPodName,
					Namespace: agentNamespace,
				},
			},
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      redfishClientPodName,
					Namespace: agentNamespace,
				},
			},
		}

		for _, obj := range objs {
			if err := k8sClient.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
				Expect(err).ToNot(HaveOccurred())
			}
		}

		By("deleting KubeVirt custom resource (kubevirt/kubevirt)")
		kv := &kubevirtv1.KubeVirt{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kubevirt",
				Namespace: "kubevirt",
			},
		}
		if err := k8sClient.Delete(ctx, kv); err != nil && !apierrors.IsNotFound(err) {
			Expect(err).ToNot(HaveOccurred())
		}
	}
})

func getClientConfig() (*rest.Config, error) {
	return clientcmd.BuildConfigFromFlags("", path.Join(os.Getenv("HOME"), ".kube", "config"))
}
