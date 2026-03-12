package virtbmccontroller

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

const (
	timeout  = time.Second * 60
	interval = time.Millisecond * 250
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

func TestVirtBMCController(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting KubeVirtBMC controller E2E test suite\n")
	RunSpecs(t, "VirtBMC Controller E2E Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("loading the built images to the Kind cluster")
	err := util.LoadImageToKindClusterWithName(controllerManagerImage, agentImage)
	Expect(err).ToNot(HaveOccurred())

	By("creating the clientsets")
	config, err = getClientConfig()
	Expect(err).ToNot(HaveOccurred())
	Expect(kubevirtv1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(bmcv1.AddToScheme(scheme.Scheme)).To(Succeed())
	k8sClient, err = client.New(config, client.Options{Scheme: scheme.Scheme})
	Expect(err).ToNot(HaveOccurred())

	if !skipKubeVirtInstall {
		By("installing KubeVirt (before cert-manager so VMs can run in e2e)")
		if !util.IsKubeVirtInstalled() {
			err = util.InstallKubeVirt()
			Expect(err).ToNot(HaveOccurred())
		} else {
			_, _ = fmt.Fprintf(GinkgoWriter, "WARNING: KubeVirt is already installed. Skipping installation...\n")
		}
	}

	if !skipCertManagerInstall {
		By("checking if cert-manager is installed already")
		isCertManagerAlreadyInstalled = util.IsCertManagerCRDsInstalled()
		if !isCertManagerAlreadyInstalled {
			_, _ = fmt.Fprintf(GinkgoWriter, "Installing cert-manager...\n")
			err = util.InstallCertManager()
			Expect(err).ToNot(HaveOccurred())
		} else {
			_, _ = fmt.Fprintf(GinkgoWriter, "WARNING: cert-manager is already installed. Skipping installation...\n")
		}
	}

	By("deploying the controller-manager")
	cmd := exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", controllerManagerImage))
	_, err = util.Run(cmd)
	Expect(err).ToNot(HaveOccurred())
})

var _ = AfterSuite(func() {
	// Delete test resources and undeploy the controller-manager; KubeVirt and cert-manager base install remain.
	if k8sClient != nil {
		ctx := context.Background()
		objs := []client.Object{
			&kubevirtv1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: util.E2EVMName, Namespace: util.E2ENamespace}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: util.E2ESecretName, Namespace: util.E2ENamespace}},
			&bmcv1.VirtualMachineBMC{ObjectMeta: metav1.ObjectMeta{Name: util.E2EBMCName, Namespace: util.E2ENamespace}},
		}
		for _, obj := range objs {
			err := k8sClient.Delete(ctx, obj)
			if err != nil && !apierrors.IsNotFound(err) {
				Expect(err).ToNot(HaveOccurred(), "delete %s/%s", obj.GetNamespace(), obj.GetName())
			}
		}
		By("waiting for test resources to be gone")
		Eventually(func() bool {
			var vm kubevirtv1.VirtualMachine
			return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKey{Namespace: util.E2ENamespace, Name: util.E2EVMName}, &vm))
		}, timeout, interval).Should(BeTrue(), "VirtualMachine %s/%s should be deleted", util.E2ENamespace, util.E2EVMName)
		Eventually(func() bool {
			var secret corev1.Secret
			return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKey{Namespace: util.E2ENamespace, Name: util.E2ESecretName}, &secret))
		}, timeout, interval).Should(BeTrue(), "Secret %s/%s should be deleted", util.E2ENamespace, util.E2ESecretName)
		Eventually(func() bool {
			var bmc bmcv1.VirtualMachineBMC
			return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKey{Namespace: util.E2ENamespace, Name: util.E2EBMCName}, &bmc))
		}, timeout, interval).Should(BeTrue(), "VirtualMachineBMC %s/%s should be deleted", util.E2ENamespace, util.E2EBMCName)
	}
	By("undeploying the controller-manager")
	cmd := exec.Command("make", "undeploy")
	_, err := util.Run(cmd)
	Expect(err).ToNot(HaveOccurred(), "make undeploy should succeed")
})

func getClientConfig() (*rest.Config, error) {
	return clientcmd.BuildConfigFromFlags("", path.Join(os.Getenv("HOME"), ".kube", "config"))
}
