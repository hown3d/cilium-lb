package main

import (
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/source"

	cilium_api_v2alpha1 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2alpha1"
	"github.com/hown3d/cilium-lb/pkg/stackit"
	"github.com/stackitcloud/stackit-sdk-go/services/iaas"
	corev1 "k8s.io/api/core/v1"
)

func (r *ruleReconciler) AddToManager(mgr manager.Manager) error {
	if r.c == nil {
		r.c = mgr.GetClient()
	}

	// if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Service{}, "spec.loadBalancerClass", func(obj client.Object) []string {
	// 	svc, ok := obj.(*corev1.Service)
	// 	if !ok {
	// 		return []string{}
	// 	}
	// 	if svc.Spec.LoadBalancerClass == nil {
	// 		return []string{}
	// 	}
	// 	return []string{*svc.Spec.LoadBalancerClass}
	// }); err != nil {
	// 	return fmt.Errorf("adding loadBalancerClass indexer: %w", err)
	// }

	return builder.
		ControllerManagedBy(mgr).
		Named("rule").
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
		}).
		// TODO: predicate to match only for our services and if ingress changes
		For(&corev1.Service{}).
		Complete(r)
}

func (r *routeReconciler) AddToManager(mgr manager.Manager) error {
	if r.c == nil {
		r.c = mgr.GetClient()
	}

	if r.iaasClient == nil {
		iaasClient, err := iaas.NewAPIClient(stackit.ClientOptions()...)
		if err != nil {
			return err
		}
		r.iaasClient = iaasClient
	}

	routeSourceChan := make(chan event.TypedGenericEvent[*cilium_api_v2alpha1.CiliumL2AnnouncementPolicy])
	routeSource := &netlinkRouteSource{
		c:          mgr.GetClient(),
		sourceChan: routeSourceChan,
	}

	if err := mgr.Add(routeSource); err != nil {
		return fmt.Errorf("adding netlink source: %w", err)
	}

	return builder.
		ControllerManagedBy(mgr).
		Named("route").
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
		}).
		// TODO: predicate to match only l2policies for our services
		For(&cilium_api_v2alpha1.CiliumL2AnnouncementPolicy{}).
		WatchesRawSource(
			source.TypedChannel(
				routeSourceChan,
				&handler.TypedEnqueueRequestForObject[*cilium_api_v2alpha1.CiliumL2AnnouncementPolicy]{},
			),
		).
		Complete(r)
}
