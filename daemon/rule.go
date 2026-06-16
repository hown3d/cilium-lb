package main

import (
	"context"
	"fmt"
	"net"

	"github.com/cilium/cilium/pkg/datapath/linux/route"
	"github.com/vishvananda/netlink"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cilium_api_v2alpha1 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2alpha1"
)

const (
	tableID = 100
)

type ruleReconciler struct {
	c client.Client
}

// Reconcile implements reconcile.TypedReconciler.
func (r *ruleReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	svc := &corev1.Service{}
	if err := r.c.Get(ctx, req.NamespacedName, svc); err != nil {
		if apierrors.IsNotFound(err) {
			// ensure routes gone
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	if ptr.Deref(svc.Spec.LoadBalancerClass, "") != cilium_api_v2alpha1.L2AnnounceLoadBalancerClass {
		return reconcile.Result{}, nil
	}

	log.V(1).Info("reconcile")

	if svc.DeletionTimestamp != nil {
		return r.delete(svc)
	}

	return r.ensure(svc)
}

func (r *ruleReconciler) ensure(svc *corev1.Service) (reconcile.Result, error) {
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		if ing.IP == "" {
			continue
		}
		if err := r.modifyRule(net.ParseIP(ing.IP), operationCreate); err != nil {
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

func (r *ruleReconciler) delete(svc *corev1.Service) (reconcile.Result, error) {
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		if ing.IP == "" {
			continue
		}
		if err := r.modifyRule(net.ParseIP(ing.IP), operationDelete); err != nil {
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

type operation string

const (
	operationCreate operation = "create"
	operationDelete operation = "delete"
)

func (r *ruleReconciler) modifyRule(ip net.IP, op operation) error {
	rule := route.Rule{
		From: &net.IPNet{
			IP:   ip,
			Mask: net.CIDRMask(32, 32),
		},
		Table: tableID,
	}
	switch op {
	case operationCreate:
		// if already exists, noop
		if err := route.ReplaceRule(rule); err != nil {
			return fmt.Errorf("replacing rule %s: %w", rule, err)
		}
	case operationDelete:
		if err := route.DeleteRule(netlink.FAMILY_V4, rule); err != nil {
			return fmt.Errorf("removing rule %s: %w", rule, err)
		}
	default:
		return fmt.Errorf("unknown operation: %s", op)
	}
	return nil
}
