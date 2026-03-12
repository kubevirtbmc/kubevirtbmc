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
