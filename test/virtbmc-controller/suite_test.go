package virtbmccontroller

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextcs "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
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
	crdClient *apiextcs.Clientset
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
	crdClient, err = apiextcs.NewForConfig(config)
	Expect(err).ToNot(HaveOccurred())
	Expect(kubevirtv1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(bmcv1.AddToScheme(scheme.Scheme)).To(Succeed())
	k8sClient, err = client.New(config, client.Options{Scheme: scheme.Scheme})
	Expect(err).ToNot(HaveOccurred())

	if !skipKubeVirtInstall {
		By("installing KubeVirt (before cert-managerso VMs can run in e2e")
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
	// Do not undeploy or delete anything so the agent e2e test (run in a separate process) can use the same cluster state.
})

func getClientConfig() (*rest.Config, error) {
	return clientcmd.BuildConfigFromFlags("", path.Join(os.Getenv("HOME"), ".kube", "config"))
}
