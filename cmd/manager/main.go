// Package main is the entrypoint of the Kanary controller manager.
//
// It wires up:
//   - scheme registration for kanary.io/v1alpha1 and core/apps APIs;
//   - manager with leader election, metrics and health endpoints;
//   - the Canary reconciler and its provider factories.
//
// Runtime flags:
//
//	--watch-namespaces           Comma-separated list. Empty = cluster-wide.
//	--leader-elect               Enable leader election (requires replicas > 1).
//	--metrics-bind-address       Metrics endpoint (default :8080).
//	--health-probe-bind-address  Healthz endpoint (default :8081).
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kanaryv1alpha1 "github.com/adrianbp/kanary-dev/api/v1alpha1"
	"github.com/adrianbp/kanary-dev/internal/controller"
	"github.com/adrianbp/kanary-dev/internal/traffic"
	"github.com/adrianbp/kanary-dev/internal/traffic/nginx"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kanaryv1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr       string
		probeAddr         string
		enableLeader      bool
		watchNamespacesCS string
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Metrics endpoint address.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Healthz endpoint address.")
	flag.BoolVar(&enableLeader, "leader-elect", false, "Enable leader election.")
	flag.StringVar(&watchNamespacesCS, "watch-namespaces", "",
		"Comma-separated list of namespaces to watch. Empty means cluster-wide.")

	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Mirror ctrl logger into stdlib slog for non-controller-runtime paths.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cacheOpts := cache.Options{}
	if ns := parseNamespaces(watchNamespacesCS); len(ns) > 0 {
		cacheOpts.DefaultNamespaces = map[string]cache.Config{}
		for _, n := range ns {
			cacheOpts.DefaultNamespaces[n] = cache.Config{}
		}
		setupLog.Info("restricting watch to namespaces", "namespaces", ns)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeader,
		LeaderElectionID:       "kanary.kanary.io",
		Cache:                  cacheOpts,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Wire traffic router factory. Providers are registered here so that
	// main.go is the single place that knows about the concrete impls.
	trafficFactory := traffic.NewFactory()
	trafficFactory.Register(kanaryv1alpha1.TrafficProviderNginx, nginx.New(mgr.GetClient()))

	if err = (&controller.CanaryReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		Recorder:       mgr.GetEventRecorderFor("kanary-controller"),
		TrafficFactory: trafficFactory,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Canary")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager",
		"version", version(),
		"metricsAddr", metricsAddr,
		"probeAddr", probeAddr,
		"leaderElection", enableLeader,
	)

	ctx := ctrl.SetupSignalHandler()
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// parseNamespaces splits a comma-separated list and trims whitespace, dropping empties.
func parseNamespaces(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// version returns the build version injected via -ldflags at build time.
var buildVersion = "dev"

func version() string { return fmt.Sprintf("kanary/%s", buildVersion) }
