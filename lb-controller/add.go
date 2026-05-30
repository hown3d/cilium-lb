package main

import (
	"context"
	"slices"
	"time"

	"github.com/hown3d/cilium-lb/pkg/l2policy"
	"github.com/stackitcloud/stackit-sdk-go/services/iaas"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

func (r *reconciler) AddToManager(mgr manager.Manager) error {
	if r.c == nil {
		r.c = mgr.GetClient()
	}

	if r.region == "" {
		r.region = "eu01"
	}

	if r.iaasClient == nil {
		iaasClient, err := iaas.NewAPIClient()
		if err != nil {
			return err
		}
		r.iaasClient = iaasClient
	}

	svcPredicate := servicePredicate(
		predicate.Or(
			predicate.GenerationChangedPredicate{},
			predicate.AnnotationChangedPredicate{},
			predicate.LabelChangedPredicate{},
		),
	)
	return builder.
		ControllerManagedBy(mgr).
		Named("ports").
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
		}).
		For(&corev1.Service{}, builder.WithPredicates(svcPredicate)).
		Owns(&corev1.Service{}, builder.WithPredicates(servicePredicateIngressChanged())).
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&corev1.Node{},
			handler.TypedEnqueueRequestsFromMapFunc(
				r.nodeMapFunc(func(svc *corev1.Service) bool {
					return svcPredicate.CreateFunc(event.TypedCreateEvent[client.Object]{
						Object: svc,
					})
				}),
			),
			r.nodePredicate(),
		)).
		Complete(r)
}

func (r *reconciler) nodePredicate() predicate.TypedPredicate[*corev1.Node] {
	checkNode := func(node *corev1.Node) bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		selector, err := l2policy.NodeSelector(ctx, r.c)
		if err != nil {
			logf.Log.Error(err, "finding l2 announcement nodes")
			return false
		}
		if node.Spec.ProviderID == "" {
			return false
		}
		if !selector.Matches(labels.Set(node.Labels)) {
			return false
		}
		logf.Log.V(2).Info("node eligble for reconcile", "node", node.Name)
		return true
	}
	return predicate.TypedFuncs[*corev1.Node]{
		CreateFunc: func(e event.TypedCreateEvent[*corev1.Node]) bool {
			return checkNode(e.Object)
		},
		DeleteFunc: func(event.TypedDeleteEvent[*corev1.Node]) bool {
			return false
		},
		UpdateFunc: func(e event.TypedUpdateEvent[*corev1.Node]) bool {
			if !checkNode(e.ObjectNew) {
				return false
			}
			if e.ObjectOld.Spec.ProviderID != e.ObjectNew.Spec.ProviderID {
				logf.Log.V(2).Info("providerID changed", "node", e.ObjectNew.Name)
				return true
			}
			logf.Log.V(2).Info("node passed predicate",
				"node", e.ObjectNew.Name,
				"oldAddresses", e.ObjectOld.Status.Addresses,
				"newAddresses", e.ObjectNew.Status.Addresses)
			return true
		},
		GenericFunc: func(event.TypedGenericEvent[*corev1.Node]) bool {
			return false
		},
	}
}

func servicePredicate(subPrediacte predicate.Predicate) predicate.Funcs {
	checkService := func(svc *corev1.Service) bool {
		if ptr.Deref(svc.Spec.LoadBalancerClass, "") != LoadBalancerClass {
			return false
		}
		if _, ok := svc.Labels["cilium.lbaas/network"]; !ok {
			return false
		}
		return true
	}

	return predicate.Funcs{
		UpdateFunc: func(e event.TypedUpdateEvent[client.Object]) bool {
			newSvc := e.ObjectNew.(*corev1.Service)
			if !checkService(newSvc) {
				return false
			}
			return subPrediacte.Update(e)
		},
		GenericFunc: func(event.TypedGenericEvent[client.Object]) bool {
			return false
		},
		DeleteFunc: func(event.TypedDeleteEvent[client.Object]) bool {
			return false
		},
		CreateFunc: func(e event.TypedCreateEvent[client.Object]) bool {
			return checkService(e.Object.(*corev1.Service))
		},
	}
}

func servicePredicateIngressChanged() predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.TypedUpdateEvent[client.Object]) bool {
			oldSvc := e.ObjectOld.(*corev1.Service)
			newSvc := e.ObjectNew.(*corev1.Service)
			if !equality.Semantic.DeepEqual(oldSvc.Status.LoadBalancer.Ingress, newSvc.Status.LoadBalancer.Ingress) {
				logf.Log.V(1).Info("load balancer ingresses are different, enqueing",
					"oldSvc", client.ObjectKeyFromObject(oldSvc),
					"newSvc", client.ObjectKeyFromObject(newSvc),
					"oldIngress", oldSvc.Status.LoadBalancer.Ingress,
					"newIngress", newSvc.Status.LoadBalancer.Ingress)
				return true
			}
			return false
		},
		GenericFunc: func(event.TypedGenericEvent[client.Object]) bool {
			return false
		},
		DeleteFunc: func(event.TypedDeleteEvent[client.Object]) bool {
			return false
		},
		CreateFunc: func(e event.TypedCreateEvent[client.Object]) bool {
			return false
		},
	}
}

func (r *reconciler) nodeMapFunc(shouldEnqueueServiceFunc func(svc *corev1.Service) bool) handler.TypedMapFunc[*corev1.Node, reconcile.Request] {
	return func(ctx context.Context, n *corev1.Node) []reconcile.Request {
		log := logf.FromContext(ctx)
		svcList := &corev1.ServiceList{}
		selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{
					Key:      "cilium.lbaas/network",
					Operator: metav1.LabelSelectorOpExists,
				},
			},
		})
		if err != nil {
			log.Error(err, "creating selector")
			return []reconcile.Request{}
		}
		if err := r.c.List(ctx, svcList, client.MatchingLabelsSelector{Selector: selector}); err != nil {
			log.Error(err, "listing services")
			return []reconcile.Request{}
		}

		requests := make([]reconcile.Request, 0, len(svcList.Items))
		for _, svc := range svcList.Items {
			if shouldEnqueueServiceFunc(&svc) {
				requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&svc)})
			}
		}
		return slices.Clip(requests)
	}
}
