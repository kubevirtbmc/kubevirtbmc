package util

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func LoadImageToKindClusterWithName(names ...string) error {
	cluster := "kvbmc-e2e"
	if v, ok := os.LookupEnv("KIND_CLUSTER"); ok {
		cluster = v
	}
	kindOptions := append([]string{"load", "docker-image", "--name", cluster}, names...)
	cmd := exec.Command(resolveKindBinary(), kindOptions...)
	out, err := Run(cmd)
	if err != nil {
		return fmt.Errorf("kind load docker-image failed: %w\noutput: %s", err, out)
	}
	return nil
}

func resolveKindBinary() string {
	if projectDir, err := getProjectDir(); err == nil {
		local := filepath.Join(projectDir, "bin", "kind")
		if _, err := os.Stat(local); err == nil {
			return local
		}
	}
	return "kind"
}
