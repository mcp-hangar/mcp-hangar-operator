// Package main is the entrypoint for the MCP Hangar operator.
//
// It bootstraps a controller-runtime manager, registers all three reconcilers
// (MCPServer, MCPServerGroup, MCPDiscoverySource), configures health/ready
// probes, and starts the manager with leader election.
package main

import (
	"flag"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	mcpv1alpha1 "github.com/mcp-hangar/operator/api/v1alpha1"
	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/internal/controller"
	"github.com/mcp-hangar/operator/internal/health"
	"github.com/mcp-hangar/operator/internal/webhook"
	"github.com/mcp-hangar/operator/pkg/hangar"
	// Import metrics package for init()-based Prometheus registration.
	_ "github.com/mcp-hangar/operator/pkg/metrics"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(mcpv1alpha1.AddToScheme(scheme))
	utilruntime.Must(mcpv1alpha2.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr             string
		healthProbeAddr         string
		enableLeaderElection    bool
		enableWebhooks          bool
		hangarURL               string
		hangarAPIKey            string
		leaseDuration           time.Duration
		renewDeadline           time.Duration
		retryPeriod             time.Duration
		leaderElectionNamespace string
		gracefulShutdownTimeout time.Duration
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&healthProbeAddr, "health-probe-bind-address", ":8081", "The address the health probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&enableWebhooks, "enable-webhooks", false,
		"Enable validating admission webhooks. Requires cert-manager or manual TLS certificate setup.")
	flag.StringVar(&hangarURL, "hangar-url", os.Getenv("HANGAR_URL"), "URL of MCP Hangar core service (optional).")
	flag.StringVar(&hangarAPIKey, "hangar-api-key", os.Getenv("HANGAR_API_KEY"), "API key for MCP Hangar core (optional).")
	flag.DurationVar(&leaseDuration, "lease-duration", 15*time.Second,
		"Duration that non-leader candidates will wait after observing a leadership renewal "+
			"before attempting to acquire the leader lease.")
	flag.DurationVar(&renewDeadline, "renew-deadline", 10*time.Second,
		"Interval between attempts by the acting leader to renew the leader lease.")
	flag.DurationVar(&retryPeriod, "retry-period", 2*time.Second,
		"Duration between leader election retries.")
	flag.StringVar(&leaderElectionNamespace, "leader-election-namespace", "",
		"Namespace in which the leader election lease will be created. "+
			"Defaults to the namespace of the operator pod.")
	flag.DurationVar(&gracefulShutdownTimeout, "graceful-shutdown-timeout", 10*time.Second,
		"Maximum duration the manager will wait for running reconcilers to finish on shutdown.")

	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	setupLog := ctrl.Log.WithName("setup")

	mgrOpts := ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress:        healthProbeAddr,
		LeaderElection:                enableLeaderElection,
		LeaderElectionID:              "mcp-hangar-operator.mcp-hangar.io",
		LeaderElectionReleaseOnCancel: true,
		LeaseDuration:                 &leaseDuration,
		RenewDeadline:                 &renewDeadline,
		RetryPeriod:                   &retryPeriod,
		GracefulShutdownTimeout:       &gracefulShutdownTimeout,
	}

	if leaderElectionNamespace != "" {
		mgrOpts.LeaderElectionNamespace = leaderElectionNamespace
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), mgrOpts)
	if err != nil {
		setupLog.Error(err, "unable to create manager")
		os.Exit(1)
	}

	// Optional: wire Hangar core client if URL is provided.
	var hangarClient *hangar.Client
	if hangarURL != "" {
		hangarClient = hangar.NewClient(&hangar.Config{
			URL:        hangarURL,
			APIKey:     hangarAPIKey,
			Timeout:    10 * time.Second,
			MaxRetries: 3,
		})
		setupLog.Info("Hangar core client configured", "url", hangarURL)
	}

	// Register MCPServer controller.
	if err := (&controller.MCPServerReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		Recorder:     mgr.GetEventRecorderFor("mcpserver-controller"),
		HangarClient: hangarClient,
		Config:       controller.DefaultReconcilerConfig(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "MCPServer")
		os.Exit(1)
	}

	// Register MCPServerGroup controller.
	if err := (&controller.MCPServerGroupReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		Recorder:     mgr.GetEventRecorderFor("mcpservergroup-controller"),
		HangarClient: hangarClient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "MCPServerGroup")
		os.Exit(1)
	}

	// Register MCPDiscoverySource controller.
	if err := (&controller.MCPDiscoverySourceReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		Recorder:     mgr.GetEventRecorderFor("mcpdiscoverysource-controller"),
		HangarClient: hangarClient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "MCPDiscoverySource")
		os.Exit(1)
	}

	// Register validating admission webhooks and the conversion webhook.
	//
	// A validator is registered for every served version of every kind that has
	// spec constraints. v1alpha2 is the storage AND a served version, so it must
	// be validated explicitly -- writes submitted as v1alpha2 never reach the
	// v1alpha1 validator (issue #12).
	//
	// Registering the builder for the convertible MCPServer types also installs
	// the single shared "/convert" conversion handler on the webhook server, so
	// the apiserver's conversion requests (strategy: Webhook) are served and the
	// hand-written v1alpha1<->v1alpha2 ConvertTo/ConvertFrom logic is actually
	// exercised (issue #11).
	if enableWebhooks {
		type webhookReg struct {
			name      string
			obj       runtime.Object
			validator admission.CustomValidator
		}
		regs := []webhookReg{
			{"MCPServer/v1alpha1", &mcpv1alpha1.MCPServer{}, &webhook.MCPServerValidator{}},
			{"MCPServer/v1alpha2", &mcpv1alpha2.MCPServer{}, &webhook.MCPServerV1alpha2Validator{}},
			{"MCPServerGroup/v1alpha1", &mcpv1alpha1.MCPServerGroup{}, &webhook.MCPServerGroupValidator{}},
			{"MCPServerGroup/v1alpha2", &mcpv1alpha2.MCPServerGroup{}, &webhook.MCPServerGroupV1alpha2Validator{}},
			{"MCPDiscoverySource/v1alpha1", &mcpv1alpha1.MCPDiscoverySource{}, &webhook.MCPDiscoverySourceValidator{}},
			{"MCPDiscoverySource/v1alpha2", &mcpv1alpha2.MCPDiscoverySource{}, &webhook.MCPDiscoverySourceV1alpha2Validator{}},
		}
		for _, r := range regs {
			if err := ctrl.NewWebhookManagedBy(mgr).
				For(r.obj).
				WithValidator(r.validator).
				Complete(); err != nil {
				setupLog.Error(err, "unable to create webhook", "webhook", r.name)
				os.Exit(1)
			}
		}
		setupLog.Info("validating + conversion webhooks registered",
			"validatedKinds", len(regs))
	}

	// Health and readiness probes.
	// Liveness: always returns OK -- the process is alive.
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}

	// Readiness: returns OK only when this replica is the elected leader.
	// Non-leader replicas report not-ready so that Services (e.g. webhook)
	// route traffic exclusively to the active leader.
	if enableLeaderElection {
		leaderCheck := health.NewLeaderChecker(mgr.Elected())
		if err := mgr.AddReadyzCheck("readyz", leaderCheck.Check); err != nil {
			setupLog.Error(err, "unable to set up readiness check")
			os.Exit(1)
		}
	} else {
		if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
			setupLog.Error(err, "unable to set up readiness check")
			os.Exit(1)
		}
	}

	setupLog.Info("starting manager",
		"leaderElection", enableLeaderElection,
		"leaseDuration", leaseDuration,
		"renewDeadline", renewDeadline,
		"retryPeriod", retryPeriod,
		"gracefulShutdownTimeout", gracefulShutdownTimeout,
	)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
