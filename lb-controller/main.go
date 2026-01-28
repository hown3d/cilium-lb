package main

import (
	"errors"
	"fmt"
	"os"

	flag "github.com/spf13/pflag"
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
	projectID   string
	clusterName string
)

func init() {
	flag.StringVar(&projectID, "project-id", "", "STACKIT project id")
	flag.StringVar(&clusterName, "cluster-name", "kubernetes", "Kubernetes cluster name")
}

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func run() error {
	if projectID == "" {
		return errors.New("project id cannot be empty")
	}
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
	if err := (&reconciler{
		projectID:   projectID,
		clusterName: clusterName,
	}).AddToManager(mgr); err != nil {
		return err
	}
	return mgr.Start(signals.SetupSignalHandler())
}
