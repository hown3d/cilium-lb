package ciliumconversion

import (
	slimmetav1 "github.com/cilium/cilium/pkg/k8s/slim/k8s/apis/meta/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func SlimSelectorToMetaSelector(ls *slimmetav1.LabelSelector) *metav1.LabelSelector {
	if ls == nil {
		return nil
	}
	newLs := &metav1.LabelSelector{
		MatchLabels: ls.MatchLabels,
	}
	expressions := make([]metav1.LabelSelectorRequirement, 0, len(ls.MatchExpressions))
	for _, e := range ls.MatchExpressions {
		expressions = append(expressions, metav1.LabelSelectorRequirement{
			Key:      e.Key,
			Operator: metav1.LabelSelectorOperator(e.Operator),
			Values:   e.Values,
		})
	}
	newLs.MatchExpressions = expressions
	return newLs
}
