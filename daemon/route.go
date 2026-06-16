package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"

	"github.com/cilium/cilium/pkg/datapath/linux/route"
	"github.com/go-logr/logr"
	"github.com/stackitcloud/stackit-sdk-go/services/iaas"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cilium_api_v2alpha1 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2alpha1"

	"github.com/cilium/cilium/pkg/datapath/linux/safenetlink"
)

type routeReconciler struct {
	NetworkID string
	ProjectID string
	Region    string

	iaasClient *iaas.APIClient
	c          client.Client
}

// Start implements [manager.Runnable].
func (r *routeReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	l2Policy := &cilium_api_v2alpha1.CiliumL2AnnouncementPolicy{}
	if err := r.c.Get(ctx, req.NamespacedName, l2Policy); err != nil {
		return reconcile.Result{}, err
	}
	ifaces := l2Policy.Spec.Interfaces
	// empty interfaces in l2Policy means match all
	if len(ifaces) == 0 {
		links, err := safenetlink.LinkList()
		if err != nil {
			return reconcile.Result{}, err
		}
		for _, link := range links {
			device, ok := link.(*netlink.Device)
			if !ok {
				continue
			}
			ifaces = append(ifaces, device.Name)
		}
	}

	log.V(1).Info("setup routes for interfaces", "interfaces", ifaces)

	f := r.ensure
	if l2Policy.DeletionTimestamp != nil {
		f = r.delete
	}
	for _, iface := range ifaces {
		if err := f(ctx, iface); err != nil {
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

func (r *routeReconciler) ensure(ctx context.Context, iface string) error {
	log := logf.FromContext(ctx)

	gateway, err := r.getNetworkGateway(ctx)
	if err != nil {
		return fmt.Errorf("getting network gateway: %w", err)
	}

	rout := defaultRoute(iface, gateway)
	log.V(1).Info("upserting route", "route", rout)

	return route.Upsert(slog.New(logr.ToSlogHandler(log)), rout)
}

func (r *routeReconciler) delete(ctx context.Context, iface string) error {
	log := logf.FromContext(ctx)

	gateway, err := r.getNetworkGateway(ctx)
	if err != nil {
		return fmt.Errorf("getting network gateway: %w", err)
	}

	rout := defaultRoute(iface, gateway)
	log.V(1).Info("deleting route", "route", rout)

	if err := route.Delete(rout); err != nil {
		// Ignore ESRCH (no such process) and ENOENT (no such file or directory) errors,
		// which indicate the route was already deleted
		if !errors.Is(err, unix.ESRCH) && !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("failed to delete route (%+v): %w", rout, err)
		}
	}
	return nil
}

func (r *routeReconciler) getNetworkGateway(ctx context.Context) (netip.Addr, error) {
	network, err := r.iaasClient.GetNetwork(ctx, r.ProjectID, r.Region, r.NetworkID).Execute()
	if err != nil {
		return netip.Addr{}, err
	}
	gateway := network.GetIpv4().Gateway.Get()
	if gateway == nil {
		return netip.Addr{}, fmt.Errorf("network %s has no gateway", r.NetworkID)
	}
	return netip.ParseAddr(*gateway)
}

func defaultRoute(iface string, gateway netip.Addr) route.Route {
	all := net.IPNet{
		IP:   net.IPv4zero,
		Mask: net.CIDRMask(0, 32),
	}

	return route.Route{
		Nexthop: new(net.IP(gateway.AsSlice())),
		Device:  iface,
		Prefix:  all,
		Table:   tableID,
	}
}

type netlinkRouteSource struct {
	sourceChan chan<- event.TypedGenericEvent[*cilium_api_v2alpha1.CiliumL2AnnouncementPolicy]
	c          client.Client
	log        logr.Logger
}

func (s *netlinkRouteSource) Start(ctx context.Context) error {
	routeChan := make(chan netlink.RouteUpdate)
	if err := netlink.RouteSubscribe(routeChan, ctx.Done()); err != nil {
		return err
	}

	// if context is done, close channel to stop loop
	go func() {
		<-ctx.Done()
		close(s.sourceChan)
	}()

	for update := range routeChan {
		if err := s.push(ctx, update); err != nil {
			s.log.Error(err, "pushing route update")
		}
	}
	return nil
}

func (s *netlinkRouteSource) push(ctx context.Context, update netlink.RouteUpdate) error {
	// we only care if our route was deleted
	if update.Type != unix.RTM_DELROUTE {
		s.log.V(1).Info("route update was not delete, skipping")
		return nil
	}

	if update.Table != tableID {
		s.log.V(1).Info("route update was not in our table, skipping")
		return nil
	}

	link, err := netlink.LinkByIndex(update.LinkIndex)
	if err != nil {
		// Ignore ESRCH (no such process) and ENOENT (no such file or directory) errors,
		// which indicate the link is not present
		if !errors.Is(err, unix.ESRCH) && !errors.Is(err, unix.ENOENT) {
			return err
		} else {
			return nil
		}
	}

	log := s.log.WithValues("iface", link.Attrs().Name)

	log.V(1).Info("checking matching policies")
	policyList := &cilium_api_v2alpha1.CiliumL2AnnouncementPolicyList{}
	if err := s.c.List(ctx, policyList); err != nil {
		return err
	}
	policies := slices.DeleteFunc(policyList.Items, func(policy cilium_api_v2alpha1.CiliumL2AnnouncementPolicy) bool {
		// matches all interfaces
		if len(policy.Spec.Interfaces) == 0 {
			return false
		}
		return !slices.Contains(policy.Spec.Interfaces, link.Attrs().Name)
	})

	for _, pol := range policies {
		log.V(1).Info("route delete matched policy", "policy", client.ObjectKeyFromObject(&pol))
		s.sourceChan <- event.TypedGenericEvent[*cilium_api_v2alpha1.CiliumL2AnnouncementPolicy]{
			Object: &pol,
		}
	}
	return nil
}
