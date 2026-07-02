package main

import (
	"flag"
	"os"

	openbaov1alpha1 "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/api/v1alpha1"
	"github.com/guardian-intelligence/guardian/src/services/secrets/openbao/operator/controllers"
	bao "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/operator/openbao"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var scheme = runtime.NewScheme()

func init() {
	must(openbaov1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	var leaderElect bool
	var reconcileModeRaw string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "address for controller metrics")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "address for health probes")
	flag.BoolVar(&leaderElect, "leader-elect", true, "enable leader election")
	flag.StringVar(&reconcileModeRaw, "reconcile-mode", envOrDefault("OPENBAO_RECONCILE_MODE", string(controllers.ReconcileModeObserve)), "OpenBao reconciliation mode: observe or apply")
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("openbao-ops-controller")

	if _, err := bao.NewClientFromEnv(); err != nil {
		log.Error(err, "configure OpenBao client")
		os.Exit(1)
	}
	reconcileMode, err := controllers.ParseReconcileMode(reconcileModeRaw)
	if err != nil {
		log.Error(err, "configure reconcile mode")
		os.Exit(1)
	}

	managerOptions := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "openbao-ops-controller.guardian.dev",
	}
	if namespace := os.Getenv("WATCH_NAMESPACE"); namespace != "" {
		managerOptions.Cache = cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				namespace: {},
			},
		}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), managerOptions)
	if err != nil {
		log.Error(err, "create manager")
		os.Exit(1)
	}

	must((&controllers.PolicyReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Mode: reconcileMode}).SetupWithManager(mgr))
	must((&controllers.KubernetesAuthRoleReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Mode: reconcileMode}).SetupWithManager(mgr))
	must((&controllers.AuthBackendReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Mode: reconcileMode}).SetupWithManager(mgr))
	must((&controllers.MountReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Mode: reconcileMode}).SetupWithManager(mgr))
	must((&controllers.MountTuneReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Mode: reconcileMode}).SetupWithManager(mgr))

	must(mgr.AddHealthzCheck("healthz", healthz.Ping))
	must(mgr.AddReadyzCheck("readyz", healthz.Ping))

	log.Info("starting manager", "reconcileMode", reconcileMode.String())
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "run manager")
		os.Exit(1)
	}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func envOrDefault(name string, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
