package main

import (
	"context"
	"fmt"
	"maps"
	"net/netip"
	"slices"

	cilium_api_v2 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
	cilium_api_v2alpha1 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2alpha1"
	v1 "github.com/cilium/cilium/pkg/k8s/slim/k8s/apis/meta/v1"
	"github.com/stackitcloud/stackit-sdk-go/services/iaas"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (r *reconciler) ensure(ctx context.Context, svc *corev1.Service, network string) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	internalSvc, err := r.createOrUpdateInternalService(ctx, svc)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("creating internal loadbalancer service: %w", err)
	}

	ingressMap := map[netip.Addr]corev1.LoadBalancerIngress{}
	log.V(1).Info("checking loadbalancer ingresses", "ingresses", internalSvc.Status.LoadBalancer.Ingress)
	for _, ing := range internalSvc.Status.LoadBalancer.Ingress {
		if ing.IP == "" {
			continue
		}

		addr, err := netip.ParseAddr(ing.IP)
		if err != nil {
			log.Error(err, "parsing ingress IP")
			continue
		}
		log := log.WithValues("ip", addr)

		// exists, err := isPublicIPOfIPPool(ctx, r.c, svc, addr)
		// if err != nil {
		// 	return reconcile.Result{}, err
		// }
		// if exists {
		// 	continue
		// }

		log.V(1).Info("ensuring port")
		nicID, err := r.ensurePort(ctx, svc, network, addr)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("ensuring port: %w", err)
		}

		log.V(1).Info("ensuring allowed addresses")
		if err := r.ensureAllowedAddresses(ctx, network, addr); err != nil {
			return reconcile.Result{}, fmt.Errorf("adding allowed address to nodes: %w", err)
		}

		log.V(1).Info("ensuring public ip")
		pubIP, err := r.ensurePublicIP(ctx, svc, nicID)
		if err != nil {
			return reconcile.Result{}, err
		}

		// if err := r.persistPublicIPInLBPool(ctx, svc, pubIP); err != nil {
		// 	return reconcile.Result{}, fmt.Errorf("persisting public ip in pool: %w", err)
		// }

		lbIngress := corev1.LoadBalancerIngress{
			IP:     pubIP.String(),
			IPMode: ing.IPMode,
			Ports:  ing.Ports,
		}
		ingressMap[pubIP] = lbIngress
		// idx := slices.IndexFunc(svc.Status.LoadBalancer.Ingress, func(ingress corev1.LoadBalancerIngress) bool {
		// 	return ingress.IP == pubIP.String()
		// })
		// if idx == -1 {
		// 	svc.Status.LoadBalancer.Ingress = append([]corev1.LoadBalancerIngress{lbIngress}, svc.Status.LoadBalancer.Ingress...)
		// } else {
		// 	svc.Status.LoadBalancer.Ingress[idx] = lbIngress
		// }
	}

	before := svc.DeepCopy()
	svc.Status.LoadBalancer.Ingress = slices.Collect(maps.Values(ingressMap))
	if !equality.Semantic.DeepEqual(before.Status.LoadBalancer.Ingress, svc.Status.LoadBalancer.Ingress) {
		log.V(1).Info("loadbalancer ingress unequal, patching")
		if err := r.c.Status().Patch(ctx, svc, client.MergeFrom(before)); err != nil {
			if apierrors.IsConflict(err) {
				log.V(1).Info("conflict on status update, requeueing")
				return reconcile.Result{Requeue: true}, nil
			}
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

func (r *reconciler) ensureAllowedAddresses(ctx context.Context, network string, ip netip.Addr) error {
	log := logf.FromContext(ctx)
	nodes, err := r.l2AnnouncementNodes(ctx)
	if err != nil {
		return fmt.Errorf("getting node selector from l2 announcements: %w", err)
	}

	g, gCtx := errgroup.WithContext(ctx)
	for _, node := range nodes {
		log.V(1).Info("ensuring allowed address on node", "node", client.ObjectKeyFromObject(&node))
		g.Go(func() error {
			return r.ensureAllowedAddressOnNode(gCtx, &node, network, ip)
		})
	}

	return g.Wait()
}

func (r *reconciler) ensureAllowedAddressOnNode(ctx context.Context, node *corev1.Node, network string, ip netip.Addr) error {
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
		found := slices.ContainsFunc(addresses, func(address iaas.AllowedAddressesInner) bool {
			if address.String == nil {
				return false
			}
			addr, err := netip.ParsePrefix(*address.String)
			if err != nil {
				return false
			}
			return addr.Addr().Compare(ip) == 0
		})
		if found {
			log.V(1).Info("node already has address in allowedAddresses, skipping")
			continue
		}

		log.V(1).Info("updating allowed addresses")
		addresses = append(addresses, iaas.StringAsAllowedAddressesInner(ptr.To(netip.PrefixFrom(ip, 32).String())))
		_, err := r.iaasClient.UpdateNic(ctx, r.projectID, r.region, network, nic.GetId()).UpdateNicPayload(iaas.UpdateNicPayload{
			AllowedAddresses: &addresses,
		}).Execute()
		if err != nil {
			return fmt.Errorf("updating allowed addresses on nic %s: %w", nic.GetId(), err)
		}
	}
	return nil
}

const l2announcementPolicyName = "loadbalancer"

func (r *reconciler) l2AnnouncementNodeSelector(ctx context.Context) (labels.Selector, error) {
	l2announcementPolicy := &cilium_api_v2alpha1.CiliumL2AnnouncementPolicy{
		ObjectMeta: metav1.ObjectMeta{
			// TODO: find a good way to determine which l2announcement policies to use.
			// Probably best to use a list and check also if there are service selectors present.
			// For now, we can just go hardcoded
			Name: l2announcementPolicyName,
		},
	}
	if err := r.c.Get(ctx, client.ObjectKeyFromObject(l2announcementPolicy), l2announcementPolicy); err != nil {
		return nil, err
	}

	var (
		selector labels.Selector
		err      error
	)
	labelSelector := slimSelectorToMetaSelector(l2announcementPolicy.Spec.NodeSelector)
	// In cilium l2 announcements, no selector means all nodes
	if labelSelector == nil {
		selector = labels.Everything()
	} else {
		selector, err = metav1.LabelSelectorAsSelector(labelSelector)
		if err != nil {
			return nil, err
		}
	}
	return selector, nil
}

func (r *reconciler) l2AnnouncementNodes(ctx context.Context) ([]corev1.Node, error) {
	selector, err := r.l2AnnouncementNodeSelector(ctx)
	if err != nil {
		return nil, err
	}
	nodes := &corev1.NodeList{}
	if err := r.c.List(ctx, nodes, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, err
	}
	return nodes.Items, nil
}

func (r *reconciler) persistPublicIPInLBPool(ctx context.Context, svc *corev1.Service, pubIP netip.Addr) error {
	lbIPPool := publicIPPoolForSvc(svc)
	_, err := controllerutil.CreateOrUpdate(ctx, r.c, lbIPPool, func() error {
		lbIPPool.Spec.Blocks = []cilium_api_v2.CiliumLoadBalancerIPPoolIPBlock{
			{
				Cidr: cilium_api_v2.IPv4orIPv6CIDR(netip.PrefixFrom(pubIP, 32).String()),
			},
		}
		lbIPPool.Spec.ServiceSelector = v1.SetAsLabelSelector(map[string]string{
			"io.kubernetes.service.name":      svc.Name,
			"io.kubernetes.service.namespace": svc.Namespace,
		})
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func publicIPPoolForSvc(svc *corev1.Service) *cilium_api_v2.CiliumLoadBalancerIPPool {
	return &cilium_api_v2.CiliumLoadBalancerIPPool{
		ObjectMeta: metav1.ObjectMeta{
			Name: identifierFromSvc(svc),
		},
	}
}

func isPublicIPOfIPPool(ctx context.Context, c client.Client, svc *corev1.Service, pubIP netip.Addr) (bool, error) {
	lbPool := publicIPPoolForSvc(svc)
	if err := c.Get(ctx, client.ObjectKeyFromObject(lbPool), lbPool); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return slices.ContainsFunc(lbPool.Spec.Blocks, func(block cilium_api_v2.CiliumLoadBalancerIPPoolIPBlock) bool {
		return block.Cidr == cilium_api_v2.IPv4orIPv6CIDR(netip.PrefixFrom(pubIP, 32).String())
	}), nil
}

func (r *reconciler) ensurePort(ctx context.Context, svc *corev1.Service, networkID string, ip netip.Addr) (string, error) {
	ls := defaultLabels(svc, r.clusterName)
	nics, err := r.iaasClient.ListNics(ctx, r.projectID, r.region, networkID).LabelSelector(ls.String()).Execute()
	if err != nil {
		return "", err
	}

	var foundNic *iaas.NIC
	for _, nic := range nics.GetItems() {
		if nic.GetIpv4() == ip.String() {
			foundNic = &nic
			continue
		}
		// nic has our identifier but is not the correct ip
		if err := r.iaasClient.DeleteNicExecute(ctx, r.projectID, r.region, networkID, nic.GetId()); err != nil {
			return "", err
		}
	}
	if foundNic != nil {
		return foundNic.GetId(), nil
	}
	nic, err := r.createPort(ctx, networkID, ip, identifierFromSvc(svc), ls)
	if err != nil {
		return "", err
	}
	return nic.GetId(), nil
}

func (r *reconciler) createPort(ctx context.Context, networkID string, ip netip.Addr, name string, labels Labels) (*iaas.NIC, error) {
	payload := iaas.CreateNicPayload{
		Labels: iaas.CreateNicPayloadGetLabelsAttributeType(&labels),
		Ipv4:   ptr.To(ip.String()),
		Name:   &name,
	}
	return r.iaasClient.CreateNic(ctx, r.projectID, r.region, networkID).CreateNicPayload(payload).Execute()
}

func (r *reconciler) ensurePublicIP(ctx context.Context, svc *corev1.Service, nicID string) (netip.Addr, error) {
	log := logf.FromContext(ctx, "nic", nicID)
	ls := defaultLabels(svc, r.clusterName)
	selector := ls.String()

	log.V(1).Info("listing public ips", "selector", selector)

	publicIPs, err := r.iaasClient.ListPublicIPs(ctx, r.projectID, r.region).LabelSelector(selector).Execute()
	if err != nil {
		return netip.Addr{}, err
	}

	for _, pubIP := range publicIPs.GetItems() {
		if ptr.Deref(pubIP.GetNetworkInterface(), "") == nicID {
			return netip.ParseAddr(pubIP.GetIp())
		}

		payload := iaas.UpdatePublicIPPayload{
			NetworkInterface: iaas.NewNullableString(&nicID),
		}
		payload.SetLabels(ls)
		newPubIP, err := r.iaasClient.UpdatePublicIP(ctx, r.projectID, r.region, pubIP.GetId()).UpdatePublicIPPayload(payload).Execute()
		if err != nil {
			return netip.Addr{}, err
		}
		return netip.ParseAddr(newPubIP.GetIp())
	}

	log.V(1).Info("public ip not found, creating")

	payload := iaas.CreatePublicIPPayload{
		NetworkInterface: iaas.NewNullableString(&nicID),
	}
	payload.SetLabels(ls)
	pubIP, err := r.iaasClient.CreatePublicIP(ctx, r.projectID, r.region).CreatePublicIPPayload(payload).Execute()
	if err != nil {
		return netip.Addr{}, err
	}
	return netip.ParseAddr(pubIP.GetIp())
}

func (r *reconciler) ensureFinalizer(ctx context.Context, svc *corev1.Service) error {
	before := svc.DeepCopy()
	if updated := controllerutil.AddFinalizer(svc, finalizer); updated {
		return r.c.Patch(ctx, svc, client.MergeFrom(before))
	}
	return nil
}

func (r *reconciler) createOrUpdateInternalService(ctx context.Context, externalSvc *corev1.Service) (*corev1.Service, error) {
	internalSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      externalSvc.Name + "-int",
			Namespace: externalSvc.Namespace,
		},
	}
	res, err := controllerutil.CreateOrUpdate(ctx, r.c, internalSvc, func() error {
		if err := controllerutil.SetOwnerReference(externalSvc, internalSvc, r.c.Scheme()); err != nil {
			return err
		}
		internalSvc.Annotations = externalSvc.Annotations
		internalSvc.Labels = externalSvc.Labels
		internalSvc.Spec = externalSvc.Spec
		internalSvc.Spec.ClusterIP = ""
		internalSvc.Spec.ClusterIPs = nil
		internalSvc.Spec.LoadBalancerClass = ptr.To(cilium_api_v2alpha1.L2AnnounceLoadBalancerClass)

		// drop node ports
		for i := range internalSvc.Spec.Ports {
			internalSvc.Spec.Ports[i].NodePort = 0
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if res != controllerutil.OperationResultNone {
		logf.FromContext(ctx).Info("internal service mutation", "operation", res)
	}
	return internalSvc, nil
}
