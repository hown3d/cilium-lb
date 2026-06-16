package main

import (
	"flag"
	"fmt"
	"os"

	runtimeutil "k8s.io/apimachinery/pkg/util/runtime"
	clientsetscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	cilium_api_v2 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
	cilium_api_v2alpha1 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2alpha1"
)

var (
	projectID string
	networkID string
)

func init() {
	flag.StringVar(&projectID, "project-id", "", "STACKIT project id")
	flag.StringVar(&networkID, "network-id", "", "STACKIT network id of loadbalancer NIC")
}

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func run() error {
	logf.SetLogger(zap.New(zap.UseDevMode(true)))
	restCfg := config.GetConfigOrDie()
	scheme := clientsetscheme.Scheme
	runtimeutil.Must(cilium_api_v2.AddToScheme(scheme))
	runtimeutil.Must(cilium_api_v2alpha1.AddToScheme(scheme))

	mgr, err := manager.New(restCfg, manager.Options{
		Scheme: scheme,
	})
	if err != nil {
		return err
	}

	if err := (&ruleReconciler{}).AddToManager(mgr); err != nil {
		return err
	}
	if err := (&routeReconciler{
		NetworkID: networkID,
		ProjectID: projectID,
		Region:    "eu01",
	}).AddToManager(mgr); err != nil {
		return err
	}
	return mgr.Start(signals.SetupSignalHandler())
}
