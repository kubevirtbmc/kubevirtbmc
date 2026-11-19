package virtbmc

import (
	"os"
	"path/filepath"
	"testing"
)

const probeKubeconfig = `apiVersion: v1
kind: Config
clusters:
- cluster: {server: "https://192.0.2.1:6443"}
  name: fake
contexts:
- context: {cluster: fake, user: fake}
  name: fake
current-context: fake
users:
- name: fake
  user: {token: fake}
`

// buildConfig must honor an explicit --kubeconfig path and, with no flag and
// no in-cluster environment, fall back to the default loading rules (probed
// via $KUBECONFIG; ~/.kube/config cannot be redirected in a test because
// client-go's RecommendedHomeFile is fixed at package init).
func TestBuildConfigResolution(t *testing.T) {
	// Outside a cluster: InClusterConfig refuses when these are empty.
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(probeKubeconfig), 0o600); err != nil {
		t.Fatalf("failed to write kubeconfig fixture: %v", err)
	}

	t.Run("explicit path wins", func(t *testing.T) {
		config, err := buildConfig(path)
		if err != nil {
			t.Fatalf("buildConfig with explicit path: %v", err)
		}
		if config.Host != "https://192.0.2.1:6443" {
			t.Fatalf("unexpected host %q", config.Host)
		}
	})

	t.Run("no flag falls back to the default loading rules", func(t *testing.T) {
		t.Setenv("KUBECONFIG", path)
		config, err := buildConfig("")
		if err != nil {
			t.Fatalf("buildConfig without flag: %v", err)
		}
		if config.Host != "https://192.0.2.1:6443" {
			t.Fatalf("unexpected host %q", config.Host)
		}
	})
}
