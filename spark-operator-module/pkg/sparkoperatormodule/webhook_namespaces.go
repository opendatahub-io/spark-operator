package sparkoperatormodule

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	mutatingWebhookKind   = "MutatingWebhookConfiguration"
	validatingWebhookKind = "ValidatingWebhookConfiguration"
	namespaceNameLabel    = "kubernetes.io/metadata.name"
)

// applyWebhookJobNamespaces sets namespaceSelector on every Spark Operator
// Mutating/ValidatingWebhookConfiguration webhook to In(kubernetes.io/metadata.name, namespaces).
func applyWebhookJobNamespaces(resources []unstructured.Unstructured, namespaces []string) error {
	if len(namespaces) == 0 {
		return fmt.Errorf("job namespaces must not be empty")
	}

	for i := range resources {
		kind := resources[i].GetKind()
		if kind != mutatingWebhookKind && kind != validatingWebhookKind {
			continue
		}
		webhooks, found, err := unstructured.NestedSlice(resources[i].Object, "webhooks")
		if err != nil {
			return fmt.Errorf("reading webhooks on %s/%s: %w", kind, resources[i].GetName(), err)
		}
		if !found {
			continue
		}
		for wi, raw := range webhooks {
			webhook, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("%s/%s webhooks[%d] is not an object", kind, resources[i].GetName(), wi)
			}
			// Fresh selector per webhook so later mutation of one entry cannot
			// silently change siblings that share a map reference.
			webhook["namespaceSelector"] = namespaceSelectorForJobNamespaces(namespaces)
			webhooks[wi] = webhook
		}
		if err := unstructured.SetNestedSlice(resources[i].Object, webhooks, "webhooks"); err != nil {
			return fmt.Errorf("writing webhooks on %s/%s: %w", kind, resources[i].GetName(), err)
		}
	}
	return nil
}

func namespaceSelectorForJobNamespaces(namespaces []string) map[string]any {
	values := make([]any, len(namespaces))
	for i, ns := range namespaces {
		values[i] = ns
	}
	return map[string]any{
		"matchExpressions": []any{
			map[string]any{
				"key":      namespaceNameLabel,
				"operator": "In",
				"values":   values,
			},
		},
	}
}
