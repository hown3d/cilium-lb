package main

import (
	"context"
	"fmt"
	"net/netip"
	"slices"

	"github.com/stackitcloud/stackit-sdk-go/services/iaas"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func (r *reconciler) delete(ctx context.Context, svc *corev1.Service, network string) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	for _, ing := range svc.Status.LoadBalancer.Ingress {
		if ing.IP == "" {
			continue
		}

		addr, err := netip.ParseAddr(ing.IP)
		if err != nil {
			log.Error(err, "parsing ingress IP")
			continue
		}

		// exists, err := isPublicIPOfIPPool(ctx, r.c, svc, addr)
		// if err != nil {
		// 	return reconcile.Result{}, err
		// }
		// if exists {
		// 	continue
		// }

		log.V(1).Info("deleting port")
		if err := r.deletePort(ctx, svc, network); err != nil {
			return reconcile.Result{}, fmt.Errorf("deleting port: %w", err)
		}

		log.V(1).Info("deleting allowed address")
		if err := r.deleteAllowedAddresses(ctx, network, addr); err != nil {
			return reconcile.Result{}, fmt.Errorf("deleting allowed address from nodes: %w", err)
		}

		log.V(1).Info("deleting public ip")
		if err := r.deletePublicIP(ctx, svc); err != nil {
			return reconcile.Result{}, err
		}
	}

	// if err := r.c.Delete(ctx, publicIPPoolForSvc(svc)); err != nil {
	// 	return reconcile.Result{}, fmt.Errorf("deleting cilium IP pool with publicIP: %w", err)
	// }

	return reconcile.Result{}, r.dropFinalizer(ctx, svc)
}

func (r *reconciler) deletePort(ctx context.Context, svc *corev1.Service, network string) error {
	ls := defaultLabels(svc, r.clusterName)
	nics, err := r.iaasClient.ListNics(ctx, r.projectID, r.region, network).LabelSelector(ls.String()).Execute()
	if err != nil {
		return err
	}
	for _, nic := range nics.GetItems() {
		if err := r.iaasClient.DeleteNicExecute(ctx, r.projectID, r.region, network, nic.GetId()); err != nil {
			return err
		}
	}
	return nil
}

func (r *reconciler) deletePublicIP(ctx context.Context, svc *corev1.Service) error {
	ls := defaultLabels(svc, r.clusterName)
	publicIPs, err := r.iaasClient.ListPublicIPs(ctx, r.projectID, r.region).LabelSelector(ls.String()).Execute()
	if err != nil {
		return err
	}
	for _, pubIP := range publicIPs.GetItems() {
		if err := r.iaasClient.DeletePublicIPExecute(ctx, r.projectID, r.region, pubIP.GetId()); err != nil {
			return err
		}
	}
	return nil
}

func (r *reconciler) deleteAllowedAddresses(ctx context.Context, network string, ip netip.Addr) error {
	nodes, err := r.l2AnnouncementNodes(ctx)
	if err != nil {
		return err
	}
	g, gCtx := errgroup.WithContext(ctx)
	for _, node := range nodes {
		g.Go(func() error {
			return r.deleteAllowedAddressOnNode(gCtx, &node, network, ip)
		})
	}
	return g.Wait()
}

func (r *reconciler) deleteAllowedAddressOnNode(ctx context.Context, node *corev1.Node, network string, ip netip.Addr) error {
	log := logf.FromContext(ctx).WithValues("node", client.ObjectKeyFromObject(node), "ip", ip)
	id := serverIDFromNode(node)
	if id == "" {
		log.Info("node has no stackit provider ID, skipping")
		return nil
	}

	resp, err := r.iaasClient.ListServerNICsExecute(ctx, r.projectID, r.region, id)
	if err != nil {
		return err
	}
	for _, nic := range resp.GetItems() {
		if nic.GetNetworkId() != network {
			log.V(1).Info("nic not from desired network, skipping", "nic", nic.GetId())
			continue
		}
		addresses := nic.GetAllowedAddresses()
		idx := slices.IndexFunc(addresses, func(address iaas.AllowedAddressesInner) bool {
			if address.String == nil {
				return false
			}
			addr, err := netip.ParsePrefix(*address.String)
			if err != nil {
				return false
			}
			return addr.Addr().Compare(ip) == 0
		})
		if idx == -1 {
			log.V(1).Info("address not found for node, skipping")
			continue
		}

		log.V(1).Info("deleting allowed address")
		_, err := r.iaasClient.UpdateNic(ctx, r.projectID, r.region, network, nic.GetId()).UpdateNicPayload(iaas.UpdateNicPayload{
			AllowedAddresses: ptr.To(slices.Delete(addresses, idx, idx+1)),
		}).Execute()
		if err != nil {
			return fmt.Errorf("updating allowed addresses on nic %s: %w", nic.GetId(), err)
		}
	}
	return nil
}

func (r *reconciler) dropFinalizer(ctx context.Context, svc *corev1.Service) error {
	before := svc.DeepCopy()
	if updated := controllerutil.RemoveFinalizer(svc, finalizer); updated {
		return r.c.Patch(ctx, svc, client.MergeFrom(before))
	}
	return nil
}
