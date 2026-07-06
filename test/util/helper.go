package util

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	certManagerURLFmt      = "https://github.com/jetstack/cert-manager/releases/download/%s/cert-manager.yaml"
	kubeVirtStableVersion  = "https://storage.googleapis.com/kubevirt-prow/release/kubevirt/kubevirt/stable.txt"
	kubeVirtOperatorURLFmt = "https://github.com/kubevirt/kubevirt/releases/download/%s/kubevirt-operator.yaml"
	kubeVirtCRURLFmt       = "https://github.com/kubevirt/kubevirt/releases/download/%s/kubevirt-cr.yaml"

	// NADCRDYAML is the NetworkAttachmentDefinition CRD definition.
	// We apply only the CRD (not the full Multus daemonset) so the
	// k8s.v1.cni.cncf.io/networks pod annotation is pure metadata —
	// no actual CNI plugin runs, the pod starts normally with kindnet.
	NADCRDYAML = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: network-attachment-definitions.k8s.cni.cncf.io
spec:
  group: k8s.cni.cncf.io
  scope: Namespaced
  names:
    plural: network-attachment-definitions
    singular: network-attachment-definition
    kind: NetworkAttachmentDefinition
    shortNames:
      - nad
      - net-attach-def
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          description: 'NetworkAttachmentDefinition is a CRD schema specified by the Network Plumbing Working Group to express the intent for attaching pods to one or more logical or physical networks. More information available at: https://github.com/k8snetworkplumbingwg/multi-net-spec'
          type: object
          properties:
            apiVersion:
              description: 'APIVersion defines the versioned schema of this representation of an object. Servers should convert recognized schemas to the latest internal value, and may reject unrecognized values. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources'
              type: string
            kind:
              description: 'Kind is a string value representing the REST resource this object represents. Servers may infer this from the endpoint the client submits requests to. Cannot be updated. In CamelCase. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds'
              type: string
            metadata:
              type: object
            spec:
              description: 'NetworkAttachmentDefinition spec defines the desired state of a network attachment'
              type: object
              properties:
                config:
                  description: 'NetworkAttachmentDefinition config is a JSON-formatted CNI configuration'
                  type: string`
)

var (
	certManagerVersion = os.Getenv("CERT_MANAGER_VERSION")
)

func warnError(err error) {
	_, _ = fmt.Fprintf(GinkgoWriter, "WARNING: %v\n", err)
}

func Run(cmd *exec.Cmd) (string, error) {
	dir, _ := getProjectDir()
	cmd.Dir = dir

	command := strings.Join(cmd.Args, " ")
	_, _ = fmt.Fprintf(GinkgoWriter, "Running command in %s: %s\n", cmd.Dir, command)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

func isKVMAvailable() bool {
	_, err := os.Stat("/dev/kvm")
	return err == nil
}

func IsCertManagerCRDsInstalled() bool {
	// List of common Cert Manager CRDs
	certManagerCRDs := []string{
		"certificates.cert-manager.io",
		"issuers.cert-manager.io",
		"clusterissuers.cert-manager.io",
		"certificaterequests.cert-manager.io",
		"orders.acme.cert-manager.io",
		"challenges.acme.cert-manager.io",
	}

	// Execute the kubectl command to get all CRDs
	cmd := exec.Command("kubectl", "get", "crds", "--no-headers", "-o", "custom-columns=NAME:.metadata.name")
	output, err := Run(cmd)
	if err != nil {
		return false
	}

	// Check if any of the cert-manager CRDs are present
	crdList := getNonEmptyLines(output)
	for _, crd := range certManagerCRDs {
		for _, line := range crdList {
			if line == crd {
				return true
			}
		}
	}

	return false
}

// Virtual media mount requires CDI for DataVolumes.
func IsCDIInstalled() bool {
	cmd := exec.Command("kubectl", "get", "crd", "datavolumes.cdi.kubevirt.io", "--no-headers", "-o", "name")
	output, err := Run(cmd)
	if err != nil {
		return false
	}
	return strings.TrimSpace(output) == "customresourcedefinition.apiextensions.k8s.io/datavolumes.cdi.kubevirt.io"
}

// DeclarativeHotplugVolumes feature gate is enabled. Virtual media mount requires this gate.
func HasDeclarativeHotplugVolumesEnabled() bool {
	cmd := exec.Command("kubectl", "get", "kubevirt", "kubevirt", "-n", "kubevirt",
		"-o", "jsonpath={.spec.configuration.developerConfiguration.featureGates}", "--ignore-not-found")
	output, err := Run(cmd)
	if err != nil || output == "" {
		return false
	}
	return strings.Contains(output, "DeclarativeHotplugVolumes")
}

func VirtualMediaPrerequisitesMet() bool {
	return IsCDIInstalled() && HasDeclarativeHotplugVolumesEnabled()
}

// IsNADCRDInstalled checks whether the NetworkAttachmentDefinition CRD is
// already registered in the cluster.
func IsNADCRDInstalled() bool {
	cmd := exec.Command("kubectl", "get", "crd",
		"network-attachment-definitions.k8s.cni.cncf.io",
		"--no-headers", "-o", "name", "--ignore-not-found")
	output, err := Run(cmd)
	if err != nil {
		return false
	}
	return strings.TrimSpace(output) == "customresourcedefinition.apiextensions.k8s.io/network-attachment-definitions.k8s.cni.cncf.io"
}

// ApplyNADCRD registers the NetworkAttachmentDefinition CRD in the cluster.
// No Multus daemonset is installed — the k8s.v1.cni.cncf.io/networks annotation
// on pods is pure metadata. Pods start normally with the default CNI (kindnet).
func ApplyNADCRD() error {
	if IsNADCRDInstalled() {
		_, _ = fmt.Fprintf(GinkgoWriter, "WARNING: NAD CRD is already installed. Skipping...\n")
		return nil
	}

	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(NADCRDYAML)
	dir, _ := getProjectDir()
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("apply NAD CRD: %w\noutput: %s", err, string(out))
	}
	_, _ = fmt.Fprintf(GinkgoWriter, "Applied NAD CRD: %s\n", string(out))
	return nil
}

// CreateTestNetworkAttachmentDefinition creates a minimal bridge-based
// NetworkAttachmentDefinition that e2e tests can reference via NetworkRef.
func CreateTestNetworkAttachmentDefinition(namespace string) error {
	nadYAML := fmt.Sprintf(`apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: test-multus-network
  namespace: %s
spec:
  config: '{"cniVersion":"0.3.1","name":"test-multus-network","type":"bridge","bridge":"br-test"}'
`, namespace)

	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(nadYAML)
	dir, _ := getProjectDir()
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("create NetworkAttachmentDefinition: %w\noutput: %s", err, string(out))
	}
	_, _ = fmt.Fprintf(GinkgoWriter, "Created NetworkAttachmentDefinition: %s\n", string(out))
	return nil
}

func IsKubeVirtInstalled() bool {
	cmd := exec.Command("kubectl", "get", "kubevirt", "kubevirt", "-n", "kubevirt",
		"-o", "jsonpath={.status.phase}", "--ignore-not-found")
	output, err := Run(cmd)
	if err != nil || output == "" {
		return false
	}
	return strings.TrimSpace(output) == "Deployed"
}

func InstallKubeVirt() error {
	cmd := exec.Command("curl", "-sL", kubeVirtStableVersion)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("get KubeVirt version: %w", err)
	}
	version := strings.TrimSpace(string(out))
	if version == "" {
		return fmt.Errorf("empty KubeVirt version from %s", kubeVirtStableVersion)
	}

	operatorURL := fmt.Sprintf(kubeVirtOperatorURLFmt, version)
	cmd = exec.Command("kubectl", "apply", "-f", operatorURL)
	if _, err := Run(cmd); err != nil {
		return fmt.Errorf("apply KubeVirt operator: %w", err)
	}

	crURL := fmt.Sprintf(kubeVirtCRURLFmt, version)
	cmd = exec.Command("kubectl", "apply", "-f", crURL)
	if _, err := Run(cmd); err != nil {
		return fmt.Errorf("apply KubeVirt CR: %w", err)
	}

	// Enable emulation only when KVM is not available.
	if !isKVMAvailable() {
		fmt.Println("KVM not available, enabling KubeVirt useEmulation")
		cmd = exec.Command("kubectl", "patch", "kubevirt", "kubevirt", "-n", "kubevirt", "--type=merge",
			"-p", `{"spec":{"configuration":{"developerConfiguration":{"useEmulation":true}}}}`)
		if _, err := Run(cmd); err != nil {
			return fmt.Errorf("patch KubeVirt CR for useEmulation: %w", err)
		}
	} else {
		fmt.Println("KVM is available, skipping useEmulation")
	}

	Eventually(func() (string, error) {
		cmd := exec.Command("kubectl", "get", "kubevirt", "kubevirt", "-n", "kubevirt", "-o", "jsonpath={.status.phase}")
		out, err := Run(cmd)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(out), nil
	}, "5m", "5s").Should(Equal("Deployed"), "KubeVirt should reach Deployed phase")

	return nil
}

func InstallCertManager() error {
	url := fmt.Sprintf(certManagerURLFmt, certManagerVersion)
	cmd := exec.Command("kubectl", "apply", "-f", url)
	if _, err := Run(cmd); err != nil {
		return err
	}

	cmd = exec.Command("kubectl", "wait", "deployment.apps/cert-manager-webhook",
		"--for", "condition=Available",
		"--namespace", "cert-manager",
		"--timeout", "5m",
	)
	_, err := Run(cmd)
	return err
}

func UninstallCertManager() {
	url := fmt.Sprintf(certManagerURLFmt, certManagerVersion)
	cmd := exec.Command("kubectl", "delete", "-f", url)
	if _, err := Run(cmd); err != nil {
		warnError(err)
	}
}

func getProjectDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return wd, err
	}
	for _, suffix := range []string{"/test/virtbmc-controller", "/test/virtbmc-agent"} {
		if idx := strings.Index(wd, suffix); idx != -1 {
			return wd[:idx], nil
		}
	}
	return wd, nil
}

func getNonEmptyLines(output string) []string {
	var res []string
	elements := strings.Split(output, "\n")
	for _, element := range elements {
		if element != "" {
			res = append(res, element)
		}
	}

	return res
}
