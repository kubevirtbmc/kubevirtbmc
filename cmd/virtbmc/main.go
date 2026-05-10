package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	"kubevirt.io/kubevirtbmc/pkg/virtbmc"
)

var (
	AppVersion = "dev"
	GitCommit  = "none"
)

func main() {
	var options virtbmc.Options

	app := &cli.App{
		Name:  "virtbmc",
		Usage: "receive ipmi requests and traslate them into native k8s api calls",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "address",
				Aliases:     []string{"a"},
				Value:       "127.0.0.1",
				Usage:       "listen on `IP ADDRESS`",
				Destination: &options.Address,
			},
			&cli.StringFlag{
				Name:        "kubeconfig",
				Aliases:     []string{"k"},
				Value:       "/root/.kube/config",
				Usage:       "use a specific kubeconfig `FILE`",
				Destination: &options.KubeconfigPath,
			},
			&cli.IntFlag{
				Name:        "ipmi-port",
				Value:       10623,
				Usage:       "listen on `IPMI PORT`",
				Destination: &options.IPMIPort,
			},
			&cli.IntFlag{
				Name:        "redfish-port",
				Value:       10080,
				Usage:       "listen on `REDFISH PORT`",
				Destination: &options.RedfishPort,
			},
			&cli.BoolFlag{
				Name:        "enable-ipmi",
				Value:       false,
				Usage:       "enable IPMI support",
				Destination: &options.EnableIPMI,
			},
			&cli.BoolFlag{
				Name:    "version",
				Aliases: []string{"v"},
				Usage:   "print the version",
				Action: func(c *cli.Context, b bool) error {
					if b {
						fmt.Println("Version:", AppVersion)
						fmt.Println("Git commit:", GitCommit)
						os.Exit(0)
					}
					return nil
				},
			},
		},
		Action: func(cCtx *cli.Context) error {
			options.BMCUser = os.Getenv("BMC_USERNAME")
			options.BMCPassword = os.Getenv("BMC_PASSWORD")

			if options.BMCUser == "" || options.BMCPassword == "" {
				panic("BMC credentials missing: both BMC_USERNAME and BMC_PASSWORD must be provided")
			}

			ctx := context.WithValue(cCtx.Context, virtbmc.VMNamespaceKey{}, cCtx.Args().Get(0))
			ctx = context.WithValue(ctx, virtbmc.VMNameKey{}, cCtx.Args().Get(1))
			return run(ctx, options)
		},
	}

	if err := app.Run(os.Args); err != nil {
		logrus.Fatal(err)
	}
}

func run(ctx context.Context, options virtbmc.Options) error {
	logrus.Info("Starting virtbmc")

	// TODO: check kubeconfig flag instead
	// check whether we're in a cluster or not
	_, ok := os.LookupEnv("KUBERNETES_SERVICE_HOST")

	// trap Ctrl+C can call cancel on the context
	ctx, cancel := context.WithCancel(ctx)
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
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
