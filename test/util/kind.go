package util

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func LoadImageToKindClusterWithName(names ...string) error {
	kindOptions := append([]string{"load", "docker-image", "--name", kindClusterName()}, names...)
	cmd := exec.Command(resolveKindBinary(), kindOptions...)
	out, err := Run(cmd)
	if err != nil {
		return fmt.Errorf("kind load docker-image failed: %w\noutput: %s", err, out)
	}
	return nil
}

func kindClusterName() string {
	if v, ok := os.LookupEnv("KIND_CLUSTER"); ok {
		return v
	}
	return "kvbmc-e2e"
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
