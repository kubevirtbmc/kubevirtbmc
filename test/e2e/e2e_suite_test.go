package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextcs "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
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
	timeout         = time.Second * 60
	interval        = time.Millisecond * 250
	helmReleaseName = "kubevirtbmc"
	helmNamespace   = "kubevirtbmc-system"
	helmChartPath   = "deploy/charts/kubevirtbmc"
)

var (
	skipCertManagerInstall        = os.Getenv("CERT_MANAGER_INSTALL_SKIP") == "true"
	isCertManagerAlreadyInstalled = false
	deployWithHelmChart           = os.Getenv("DEPLOY_WITH_HELM_CHART") == "true"

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

// TestE2E runs the end-to-end (e2e) test suite for the project. These tests execute in an isolated,
// temporary environment to validate project changes with the the purposed to be used in CI jobs.
// The default setup requires Kind, builds/loads the Manager Docker image locally, and installs
// CertManager.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting KubeVirtBMC end-to-end test suite\n")
	RunSpecs(t, "E2E Suite")
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

	By("creating the VirtualMachine CRD")
	err = util.CreateOrUpdateCRD(crdClient, "../../config/kubevirt-crd/kubevirt.io_virtualmachines.yaml")
	Expect(err).ToNot(HaveOccurred())
	Eventually(func() bool {
		crd, err := crdClient.ApiextensionsV1().CustomResourceDefinitions().Get(
			context.TODO(),
			"virtualmachines.kubevirt.io",
			metav1.GetOptions{},
		)
		if err != nil {
			return false
		}
		for _, cond := range crd.Status.Conditions {
			if cond.Type == apiextv1.Established && cond.Status == apiextv1.ConditionTrue {
				return true
			}
		}
		return false
	}, timeout, interval).Should(BeTrue())

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
	if deployWithHelmChart {
		imageRepo, imageTag := splitImageRef(controllerManagerImage)
		err = util.DeployHelmChart(helmReleaseName, helmNamespace, helmChartPath, imageRepo, imageTag)
	} else {
		cmd := exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", controllerManagerImage))
		_, err = util.Run(cmd)
	}
	Expect(err).ToNot(HaveOccurred())
})

var _ = AfterSuite(func() {
	var err error

	By("undeploying the controller-manager")
	if deployWithHelmChart {
		util.UninstallHelmRelease(helmReleaseName, helmNamespace)
	} else {
		cmd := exec.Command("make", "undeploy")
		_, _ = util.Run(cmd)
	}

	if !skipCertManagerInstall && !isCertManagerAlreadyInstalled {
		_, _ = fmt.Fprintf(GinkgoWriter, "Uninstalling cert-manager...\n")
		util.UninstallCertManager()
	}

	By("deleting VirtualMachine CRD")
	err = util.DeleteCRD(crdClient, "../../config/kubevirt-crd/kubevirt.io_virtualmachines.yaml")
	Expect(err).ToNot(HaveOccurred())
})

func getClientConfig() (*rest.Config, error) {
	return clientcmd.BuildConfigFromFlags("", path.Join(os.Getenv("HOME"), ".kube", "config"))
}

func splitImageRef(image string) (string, string) {
	lastColon := strings.LastIndex(image, ":")
	if lastColon == -1 {
		return image, ""
	}
	return image[:lastColon], image[lastColon+1:]
}
