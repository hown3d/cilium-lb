package main

import (
	"context"
	"fmt"
	"maps"
	"net/netip"
	"slices"

	cilium_api_v2 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
	cilium_api_v2alpha1 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2alpha1"
	"github.com/hown3d/cilium-lb/pkg/l2policy"
	"github.com/stackitcloud/stackit-sdk-go/services/iaas"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (r *reconciler) ensure(ctx context.Context, svc *corev1.Service) (reconcile.Result, error) {
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

		log.V(1).Info("ensuring port")
		nicID, err := r.ensurePort(ctx, svc, addr)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("ensuring port: %w", err)
		}

		log.V(1).Info("ensuring allowed addresses")
		if err := r.ensureAllowedAddresses(ctx, addr); err != nil {
			return reconcile.Result{}, fmt.Errorf("adding allowed address to nodes: %w", err)
		}

		log.V(1).Info("ensuring public ip")
		pubIP, err := r.ensurePublicIP(ctx, svc, nicID)
		if err != nil {
			return reconcile.Result{}, err
		}

		lbIngress := corev1.LoadBalancerIngress{
			IP:     pubIP.String(),
			IPMode: ing.IPMode,
			Ports:  ing.Ports,
		}
		ingressMap[pubIP] = lbIngress
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

func (r *reconciler) ensureAllowedAddresses(ctx context.Context, ip netip.Addr) error {
	log := logf.FromContext(ctx)
	nodes, err := r.l2AnnouncementNodes(ctx)
	if err != nil {
		return fmt.Errorf("getting node selector from l2 announcements: %w", err)
	}

	g, gCtx := errgroup.WithContext(ctx)
	for _, node := range nodes {
		log.V(1).Info("ensuring allowed address on node", "node", client.ObjectKeyFromObject(&node))
		g.Go(func() error {
			return r.ensureAllowedAddressOnNode(gCtx, &node, ip)
		})
	}

	return g.Wait()
}

func (r *reconciler) ensureAllowedAddressOnNode(ctx context.Context, node *corev1.Node, ip netip.Addr) error {
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
		if nic.GetNetworkId() != r.networkID {
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
		_, err := r.iaasClient.UpdateNic(ctx, r.projectID, r.region, r.networkID, nic.GetId()).UpdateNicPayload(iaas.UpdateNicPayload{
			AllowedAddresses: &addresses,
		}).Execute()
		if err != nil {
			return fmt.Errorf("updating allowed addresses on nic %s: %w", nic.GetId(), err)
		}
	}
	return nil
}

func (r *reconciler) l2AnnouncementNodes(ctx context.Context) ([]corev1.Node, error) {
	selector, err := l2policy.NodeSelector(ctx, r.c)
	if err != nil {
		return nil, err
	}
	nodes := &corev1.NodeList{}
	if err := r.c.List(ctx, nodes, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, err
	}
	return nodes.Items, nil
}

func publicIPPoolForSvc(svc *corev1.Service) *cilium_api_v2.CiliumLoadBalancerIPPool {
	return &cilium_api_v2.CiliumLoadBalancerIPPool{
		ObjectMeta: metav1.ObjectMeta{
			Name: identifierFromSvc(svc),
		},
	}
}

func (r *reconciler) ensurePort(ctx context.Context, svc *corev1.Service, ip netip.Addr) (string, error) {
	ls := defaultLabels(svc, r.clusterName)
	nics, err := r.iaasClient.ListNics(ctx, r.projectID, r.region, r.networkID).LabelSelector(ls.String()).Execute()
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
		if err := r.iaasClient.DeleteNicExecute(ctx, r.projectID, r.region, r.networkID, nic.GetId()); err != nil {
			return "", err
		}
	}
	if foundNic != nil {
		return foundNic.GetId(), nil
	}
	nic, err := r.createPort(ctx, ip, identifierFromSvc(svc), ls)
	if err != nil {
		return "", err
	}
	return nic.GetId(), nil
}

func (r *reconciler) createPort(ctx context.Context, ip netip.Addr, name string, labels Labels) (*iaas.NIC, error) {
	payload := iaas.CreateNicPayload{
		Labels: iaas.CreateNicPayloadGetLabelsAttributeType(&labels),
		Ipv4:   ptr.To(ip.String()),
		Name:   &name,
	}
	return r.iaasClient.CreateNic(ctx, r.projectID, r.region, r.networkID).CreateNicPayload(payload).Execute()
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
		if internalSvc.Labels == nil {
			internalSvc.Labels = map[string]string{}
		}
		internalSvc.Labels[InternalServiceLabel] = "true"

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
