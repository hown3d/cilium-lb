package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/stackitcloud/stackit-sdk-go/services/iaas"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const finalizer = "cilium.lbaas/finalizer"

type reconciler struct {
	c           client.Client
	iaasClient  *iaas.APIClient
	projectID   string
	networkID   string
	region      string
	clusterName string
}

// Reconcile implements reconcile.TypedReconciler.
func (r *reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logf.FromContext(ctx)
	log.V(1).Info("reconcile")

	svc := &corev1.Service{}
	if err := r.c.Get(ctx, req.NamespacedName, svc); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	if svc.DeletionTimestamp != nil {
		return r.delete(ctx, svc)
	}

	if err := r.ensureFinalizer(ctx, svc); err != nil {
		return reconcile.Result{}, err
	}

	return r.ensure(ctx, svc)
}

const (
	LabelIdentifier = "cilium.lbaas_identifier"
	LabelCluster    = "cilium.lbaas_cluster"
)

type Labels map[string]any

func (l Labels) String() string {
	pairs := make([]string, 0, len(l))
	for key, val := range l {
		pairs = append(pairs, fmt.Sprintf("%s=%s", key, val))
	}
	return strings.Join(pairs, ",")
}

func defaultLabels(svc *corev1.Service, clusterName string) Labels {
	return Labels{
		LabelIdentifier: identifierFromSvc(svc, clusterName),
		LabelCluster:    clusterName,
	}
}

func identifierFromSvc(svc *corev1.Service, clusterName string) string {
	return fmt.Sprintf("%s.%s.%s", clusterName, svc.Namespace, svc.Name)
}

func serverIDFromNode(node *corev1.Node) string {
	s, _ := strings.CutPrefix(node.Spec.ProviderID, "stackit://")
	return s
}
