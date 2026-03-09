package util

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/yaml"
)

const (
	certManagerURLTmpl = "https://github.com/jetstack/cert-manager/releases/download/%s/cert-manager.yaml"
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

func InstallCertManager() error {
	url := fmt.Sprintf(certManagerURLTmpl, certManagerVersion)
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
	url := fmt.Sprintf(certManagerURLTmpl, certManagerVersion)
	cmd := exec.Command("kubectl", "delete", "-f", url)
	if _, err := Run(cmd); err != nil {
		warnError(err)
	}
}

func DeployHelmChart(releaseName, namespace, chartPath, imageRepo, imageTag string) error {
	args := []string{
		"upgrade",
		"--install",
		releaseName,
		chartPath,
		"--namespace",
		namespace,
		"--create-namespace",
		"--wait",
		"--timeout",
		"5m",
	}

	if imageRepo != "" {
		args = append(args, "--set", fmt.Sprintf("image.repository=%s", imageRepo))
	}
	if imageTag != "" {
		args = append(args, "--set", fmt.Sprintf("image.tag=%s", imageTag))
	}

	cmd := exec.Command("helm", args...)
	_, err := Run(cmd)
	return err
}

func UninstallHelmRelease(releaseName, namespace string) {
	cmd := exec.Command("helm", "uninstall", releaseName, "--namespace", namespace)
	if _, err := Run(cmd); err != nil {
		warnError(err)
	}
}

func getProjectDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return wd, err
	}
	wd = strings.Replace(wd, "/test/e2e", "", -1)
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

func parseCRDYaml(relativePath string) (*apiextv1.CustomResourceDefinition, error) {
	var manifest *os.File
	var err error
	var crd apiextv1.CustomResourceDefinition

	if manifest, err = pathToOSFile(relativePath); err != nil {
		return nil, err
	}

	decoder := yaml.NewYAMLOrJSONDecoder(manifest, 100)
	for {
		var out unstructured.Unstructured
		err = decoder.Decode(&out)
		if err != nil {
			break
		}

		if out.GetKind() == "CustomResourceDefinition" {
			var marshaled []byte
			marshaled, err = out.MarshalJSON()
			if err != nil {
				break
			}
			err = json.Unmarshal(marshaled, &crd)
			break
		}
	}

	if err != io.EOF && err != nil {
		return nil, err
	}

	return &crd, nil
}

func pathToOSFile(relativePath string) (*os.File, error) {
	path, err := filepath.Abs(relativePath)
	if err != nil {
		return nil, err
	}

	return os.Open(path)
}
