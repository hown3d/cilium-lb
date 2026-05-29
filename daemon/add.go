package main

import (
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/source"

	cilium_api_v2 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
	cilium_api_v2alpha1 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2alpha1"
)

func (r *ruleReconciler) AddToManager(mgr manager.Manager) error {
	if r.c == nil {
		r.c = mgr.GetClient()
	}

	return builder.
		ControllerManagedBy(mgr).
		Named("rule").
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
		}).
		// TODO: predicate to match only for our services
		For(&cilium_api_v2.CiliumLoadBalancerIPPool{}).
		Complete(r)
}

func (r *routeReconciler) AddToManager(mgr manager.Manager) error {
	if r.c == nil {
		r.c = mgr.GetClient()
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
