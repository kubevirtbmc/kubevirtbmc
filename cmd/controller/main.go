/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
	ctlservice "kubevirt.io/kubevirtbmc/internal/controller/service"
	ctlvirtualmachinebmc "kubevirt.io/kubevirtbmc/internal/controller/virtualmachinebmc"
	webhookvirtualmachinebmc "kubevirt.io/kubevirtbmc/internal/webhook/bmc/v1beta1"

	kubevirtv1 "kubevirt.io/api/core/v1"
	//+kubebuilder:scaffold:imports
)

var (
	AppVersion = "dev"
	GitCommit  = "none"

	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kubevirtv1.AddToScheme(scheme))
	utilruntime.Must(bmcv1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var (
		metricsAddr          string
		enableLeaderElection bool
		probeAddr            string
		secureMetrics        bool
		enableHTTP2          bool
		tlsOpts              []func(*tls.Config)
		agentImageName       string
		agentImageTag        string
		agentCPURequest      string
		agentMemoryRequest   string
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8443", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&secureMetrics, "secure-metrics", true, "Enable secure (HTTPS) metrics endpoint.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false, "Enable HTTP/2 support (default: disabled for security).")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for controller manager.")
	flag.StringVar(&agentImageName, "agent-image-name", ctlvirtualmachinebmc.VirtBMCImageName, "The name of the agent image.")
	flag.StringVar(&agentImageTag, "agent-image-tag", AppVersion, "The tag of the agent image.")
	flag.StringVar(&agentCPURequest, "agent-cpu-request", ctlvirtualmachinebmc.DefaultAgentCPURequest, "The CPU request of the agent pod.")
	flag.StringVar(&agentMemoryRequest, "agent-memory-request", ctlvirtualmachinebmc.DefaultAgentMemoryRequest, "The memory request of the agent pod.")
	showVersion := flag.Bool("version", false, "Show version.")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	if *showVersion {
		fmt.Println("Version:", AppVersion)
		fmt.Println("Git commit:", GitCommit)
		os.Exit(0)
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Validate agent pod resource requests early, fail fast on misconfiguration
	if _, err := resource.ParseQuantity(agentCPURequest); err != nil {
		setupLog.Error(err, "invalid --agent-cpu-request", "value", agentCPURequest)
		os.Exit(1)
	}
	if _, err := resource.ParseQuantity(agentMemoryRequest); err != nil {
		setupLog.Error(err, "invalid --agent-memory-request", "value", agentMemoryRequest)
		os.Exit(1)
	}

	// Disable HTTP/2 unless explicitly enabled
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}
	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Create webhook server
	webhookServer := webhook.NewServer(webhook.Options{
		TLSOpts: tlsOpts,
	})

	// Configure metrics server
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	// Add built-in authn/authz filters if using secure metrics
	if secureMetrics {
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "5e66b2cf.kubevirt.io",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&ctlvirtualmachinebmc.VirtualMachineBMCReconciler{
		Client:             mgr.GetClient(),
		Scheme:             mgr.GetScheme(),
		AgentImageName:     agentImageName,
		AgentImageTag:      agentImageTag,
		AgentCPURequest:    agentCPURequest,
		AgentMemoryRequest: agentMemoryRequest,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "VirtualMachineBMC")
		os.Exit(1)
	}
	if err = (&ctlservice.ServiceReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Service")
		os.Exit(1)
	}
	if err = webhookvirtualmachinebmc.SetupVirtualMachineBMCWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to set up webhook", "webhook", "VirtualMachineBMC")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
