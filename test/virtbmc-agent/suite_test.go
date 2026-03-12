package virtbmcagent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"testing"

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
	skipCertManagerInstall = os.Getenv("CERT_MANAGER_INSTALL_SKIP") == "true"

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
	By("creating the clientsets")
	var err error
	config, err = getClientConfig()
	Expect(err).ToNot(HaveOccurred())
	Expect(kubevirtv1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(bmcv1.AddToScheme(scheme.Scheme)).To(Succeed())
	k8sClient, err = client.New(config, client.Options{Scheme: scheme.Scheme})
	Expect(err).ToNot(HaveOccurred())

})

var _ = AfterSuite(func() {
	By("undeploying the controller-manager")
	cmd := exec.Command("make", "undeploy")
	_, _ = util.Run(cmd)

	if !skipCertManagerInstall {
		_, _ = fmt.Fprintf(GinkgoWriter, "Uninstalling cert-manager (was installed by controller suite)...\n")
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
