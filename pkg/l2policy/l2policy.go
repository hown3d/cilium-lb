package l2policy

import (
	"context"

	cilium_api_v2alpha1 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2alpha1"
	"github.com/hown3d/cilium-lb/pkg/ciliumconversion"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const PolicyName = "loadbalancer"

func NodeSelector(ctx context.Context, c client.Client) (labels.Selector, error) {
	l2announcementPolicy := &cilium_api_v2alpha1.CiliumL2AnnouncementPolicy{
		ObjectMeta: metav1.ObjectMeta{
			// TODO: find a good way to determine which l2announcement policies to use.
			// Probably best to use a list and check also if there are service selectors present.
			// For now, we can just go hardcoded
			Name: PolicyName,
		},
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(l2announcementPolicy), l2announcementPolicy); err != nil {
		return nil, err
	}

	var (
		selector labels.Selector
		err      error
	)
	labelSelector := ciliumconversion.SlimSelectorToMetaSelector(l2announcementPolicy.Spec.NodeSelector)
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
