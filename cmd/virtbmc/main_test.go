package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
	"kubevirt.io/kubevirtbmc/pkg/virtbmc"
)

// runParse builds the command, neuters the action (it would require cluster
// credentials), and runs it with the given args, returning the options the
// flags landed in.
func runParse(t *testing.T, args ...string) (virtbmc.Options, error) {
	t.Helper()
	var options virtbmc.Options
	cmd := newCommand(&options)
	cmd.Action = func(context.Context, *cli.Command) error { return nil }
	err := cmd.Run(context.Background(), append([]string{"virtbmc"}, args...))
	return options, err
}

func TestConfigFileFeedsFlagsWithCommandLineTakingPrecedence(t *testing.T) {
	config := filepath.Join(t.TempDir(), "virtbmc.yaml")
	require.NoError(t, os.WriteFile(config, []byte(`
address: 10.0.0.1
redfish-port: 9999
enable-ipmi: true
storage-class: fast-sc
volume-mode: block
datavolume-size-margin: 30
virtual-media-insecure-skip-verify: true
virtual-media-ca-bundle-configmap: my-ca
`), 0o600))

	options, err := runParse(t, "--config", config, "--redfish-port", "1234", "default", "testvm")
	require.NoError(t, err)

	// Values from the file.
	require.Equal(t, "10.0.0.1", options.Address)
	require.True(t, options.EnableIPMI)
	require.Equal(t, "fast-sc", options.StorageClass)
	require.Equal(t, "block", options.VolumeMode)
	require.Equal(t, 30, options.DataVolumeSizeMargin)
	require.True(t, options.InsecureSkipVerify)
	require.Equal(t, "my-ca", options.CABundleConfigMap)
	// Command line beats the file.
	require.Equal(t, 1234, options.RedfishPort)
	// Untouched keys keep their built-in defaults.
	require.Equal(t, 10623, options.IPMIPort)
	require.False(t, options.Standalone)
}

func TestMissingConfigFileFails(t *testing.T) {
	_, err := runParse(t, "--config", filepath.Join(t.TempDir(), "no-such.yaml"), "default", "testvm")
	require.ErrorContains(t, err, "invalid --config")
}

func TestNoConfigFileKeepsDefaults(t *testing.T) {
	options, err := runParse(t, "default", "testvm")
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", options.Address)
	require.Equal(t, 10080, options.RedfishPort)
	require.Empty(t, options.StorageClass)
}
