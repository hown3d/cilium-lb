package main

import (
	"context"
	"fmt"
	"net"

	"github.com/cilium/cilium/pkg/datapath/linux/route"
	cilium_api_v2 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
	"github.com/vishvananda/netlink"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
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
	log.V(1).Info("reconcile")

	ippool := &cilium_api_v2.CiliumLoadBalancerIPPool{}
	if err := r.c.Get(ctx, req.NamespacedName, ippool); err != nil {
		if apierrors.IsNotFound(err) {
			// ensure routes gone
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	if ippool.DeletionTimestamp != nil {
		return r.delete(ctx, ippool)
	}

	return r.ensure(ctx, ippool)
}

func (r *ruleReconciler) ensure(ctx context.Context, ippool *cilium_api_v2.CiliumLoadBalancerIPPool) (reconcile.Result, error) {
	cidrs, err := blocksToIPNets(ippool.Spec.Blocks)
	if err != nil {
		return reconcile.Result{}, err
	}

	if err := r.modifyRule(cidrs, operationCreate); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (r *ruleReconciler) delete(ctx context.Context, ippool *cilium_api_v2.CiliumLoadBalancerIPPool) (reconcile.Result, error) {
	cidrs, err := blocksToIPNets(ippool.Spec.Blocks)
	if err != nil {
		return reconcile.Result{}, err
	}

	if err := r.modifyRule(cidrs, operationDelete); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

type operation string

const (
	operationCreate operation = "create"
	operationDelete operation = "delete"
)

func (r *ruleReconciler) modifyRule(cidrs []*net.IPNet, op operation) error {
	for _, cidr := range cidrs {
		rule := route.Rule{
			From:  cidr,
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
	}
	return nil
}

func blocksToIPNets(blocks []cilium_api_v2.CiliumLoadBalancerIPPoolIPBlock) ([]*net.IPNet, error) {
	cidrs := make([]*net.IPNet, 0, len(blocks))
	for _, block := range blocks {
		// TODO: support start and stop of spec
		_, cidr, err := net.ParseCIDR(string(block.Cidr))
		if err != nil {
			return nil, err
		}
		cidrs = append(cidrs, cidr)
	}
	return cidrs, nil
}
