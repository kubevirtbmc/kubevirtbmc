package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/sirupsen/logrus"
	altsrc "github.com/urfave/cli-altsrc/v3"
	"github.com/urfave/cli-altsrc/v3/yaml"
	"github.com/urfave/cli/v3"
	"kubevirt.io/kubevirtbmc/pkg/virtbmc"
)

var (
	AppVersion = "dev"
	GitCommit  = "none"
)

func main() {
	var options virtbmc.Options
	if err := newCommand(&options).Run(context.Background(), os.Args); err != nil {
		logrus.Fatal(err)
	}
}

func newCommand(options *virtbmc.Options) *cli.Command {
	// configPath backs --config; the altsrc sourcer reads it lazily, after
	// all flags are parsed, so every sourced flag sees the final value.
	var configPath string
	yamlSource := func(key string) cli.ValueSourceChain {
		return cli.NewValueSourceChain(yaml.YAML(key, altsrc.NewStringPtrSourcer(&configPath)))
	}

	cmd := &cli.Command{
		Name:  "virtbmc",
		Usage: "receive ipmi requests and traslate them into native k8s api calls",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "config",
				Usage:       "load flag defaults from YAML `FILE` (command line still wins); keys are flag names, credentials stay in env vars",
				Destination: &configPath,
			},
			&cli.StringFlag{
				Name:        "address",
				Aliases:     []string{"a"},
				Value:       "127.0.0.1",
				Usage:       "listen on `IP ADDRESS`",
				Destination: &options.Address,
				Sources:     yamlSource("address"),
			},
			&cli.StringFlag{
				Name:        "kubeconfig",
				Aliases:     []string{"k"},
				Value:       "/root/.kube/config",
				Usage:       "use a specific kubeconfig `FILE`",
				Destination: &options.KubeconfigPath,
				Sources:     yamlSource("kubeconfig"),
			},
			&cli.IntFlag{
				Name:        "ipmi-port",
				Value:       10623,
				Usage:       "listen on `IPMI PORT`",
				Destination: &options.IPMIPort,
				Sources:     yamlSource("ipmi-port"),
			},
			&cli.IntFlag{
				Name:        "redfish-port",
				Value:       10080,
				Usage:       "listen on `REDFISH PORT`",
				Destination: &options.RedfishPort,
				Sources:     yamlSource("redfish-port"),
			},
			&cli.BoolFlag{
				Name:        "enable-ipmi",
				Value:       false,
				Usage:       "enable IPMI support",
				Destination: &options.EnableIPMI,
				Sources:     yamlSource("enable-ipmi"),
			},
			&cli.StringFlag{
				Name:    "log-level",
				Value:   "info",
				Usage:   "log level (panic|fatal|error|warn|info|debug|trace)",
				Sources: yamlSource("log-level"),
			},
			&cli.BoolFlag{
				Name:        "standalone",
				Value:       false,
				Usage:       "run without the VirtualMachineBMC CRD (no controller needed); boot override state is kept in a local file",
				Destination: &options.Standalone,
				Sources:     yamlSource("standalone"),
			},
			&cli.StringFlag{
				Name:        "state-file",
				Usage:       "standalone mode: persist boot override state in `FILE` (default \"./<ns>_<vm>.json\")",
				Destination: &options.StateFile,
				Sources:     yamlSource("state-file"),
			},
			&cli.StringFlag{
				Name:        "storage-class",
				Usage:       "StorageClass for virtual media DataVolumes (default: cluster default)",
				Destination: &options.StorageClass,
				Sources:     yamlSource("storage-class"),
			},
			&cli.StringFlag{
				Name:        "volume-mode",
				Usage:       "volume mode for virtual media DataVolumes: block|filesystem (default: CDI default)",
				Destination: &options.VolumeMode,
				Sources:     yamlSource("volume-mode"),
			},
			&cli.IntFlag{
				Name:        "datavolume-size-margin",
				Usage:       "pad virtual media DataVolume size by `PERCENT` (default: 0)",
				Destination: &options.DataVolumeSizeMargin,
				Sources:     yamlSource("datavolume-size-margin"),
			},
			&cli.BoolFlag{
				Name:        "virtual-media-insecure-skip-verify",
				Value:       false,
				Usage:       "skip TLS certificate verification when fetching virtual media images over https",
				Destination: &options.InsecureSkipVerify,
				Sources:     yamlSource("virtual-media-insecure-skip-verify"),
			},
			&cli.StringFlag{
				Name:        "virtual-media-ca-bundle-configmap",
				Usage:       "ConfigMap (in the VM's namespace, key \"ca.pem\") with the CA bundle trusted when fetching virtual media images over https",
				Destination: &options.CABundleConfigMap,
				Sources:     yamlSource("virtual-media-ca-bundle-configmap"),
			},
			&cli.BoolFlag{
				Name:    "version",
				Aliases: []string{"v"},
				Usage:   "print the version",
				Action: func(ctx context.Context, cmd *cli.Command, b bool) error {
					if b {
						fmt.Println("Version:", AppVersion)
						fmt.Println("Git commit:", GitCommit)
						os.Exit(0)
					}
					return nil
				},
			},
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			// altsrc silently falls back to defaults when the file is
			// unreadable; a typo'd --config must fail loudly instead.
			if configPath != "" {
				if _, err := os.Stat(configPath); err != nil {
					return ctx, fmt.Errorf("invalid --config: %w", err)
				}
			}
			return ctx, nil
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			level, err := logrus.ParseLevel(cmd.String("log-level"))
			if err != nil {
				return fmt.Errorf("invalid --log-level: %w", err)
			}
			logrus.SetLevel(level)

			options.BMCUser = os.Getenv("BMC_USERNAME")
			options.BMCPassword = os.Getenv("BMC_PASSWORD")

			if options.BMCUser == "" || options.BMCPassword == "" {
				panic("BMC credentials missing: both BMC_USERNAME and BMC_PASSWORD must be provided")
			}

			vmNamespace := cmd.Args().Get(0)
			vmName := cmd.Args().Get(1)
			if options.Standalone && options.StateFile == "" {
				options.StateFile = defaultStateFilePath(vmNamespace, vmName)
			}

			ctx = context.WithValue(ctx, virtbmc.VMNamespaceKey{}, vmNamespace)
			ctx = context.WithValue(ctx, virtbmc.VMNameKey{}, vmName)
			options.PodName = os.Getenv("POD_NAME")
			return run(ctx, *options)
		},
	}
	return cmd
}

func defaultStateFilePath(vmNamespace, vmName string) string {
	return vmNamespace + "_" + vmName + ".json"
}

func run(ctx context.Context, options virtbmc.Options) error {
	logrus.Info("Starting virtbmc")

	// TODO: check kubeconfig flag instead
	// check whether we're in a cluster or not
	_, ok := os.LookupEnv("KUBERNETES_SERVICE_HOST")

	// trap Ctrl+C can call cancel on the context
	ctx, cancel := context.WithCancel(ctx)
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	defer func() {
		signal.Stop(c)
		cancel()
	}()
	go func() {
		select {
		case <-c:
			cancel()
		case <-ctx.Done():
		}
		<-c
		os.Exit(2)
	}()

	virtBMC, err := virtbmc.NewVirtBMC(ctx, options, ok)
	if err != nil {
		return fmt.Errorf("failed to create virtbmc server: %v", err)
	}

	return virtBMC.Run()
}
