/*
Copyright 2026 The Kubeflow authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Regression tests for RHAI-10913: Preserve deployment selector labels for
// RHOAI 3.5.x → 3.6 and ODH 3.4/3.5 → 3.6 upgrades.
//
// RHOAI 3.5 and ODH 3.4/3.5 created Deployments with 6 selector labels via
// kustomize.WithLabel(). Since Kubernetes deployment selectors are immutable,
// upgrades fail unless we preserve those labels with JSON6902 patches.
package kustomize_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// TestRHOAIOverlayPreserves35SelectorLabels verifies that the RHOAI overlay
// preserves the 6 selector labels that RHOAI 3.5 created.
func TestRHOAIOverlayPreserves35SelectorLabels(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	overlayDir := filepath.Join(repoRoot, "config", "overlays", "rhoai")
	resources := buildOverlay(t, overlayDir)

	// RHOAI 3.5 in-tree component created these selector labels via kustomize.WithLabel()
	expectedLabels := map[string]string{
		"app.kubernetes.io/name":           "spark-operator",
		"app.kubernetes.io/instance":       "spark-operator",
		"app.kubernetes.io/managed-by":     "kustomize",
		"app.kubernetes.io/part-of":        "sparkoperator",
		"app.opendatahub.io/sparkoperator": "true",
	}

	testCases := []struct {
		name      string
		component string
	}{
		{"spark-operator-controller", "controller"},
		{"spark-operator-webhook", "webhook"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Find the deployment
			var deployment *appsv1.Deployment
			for _, obj := range resources {
				if obj.GetKind() == "Deployment" && obj.GetName() == tc.name {
					deployment = &appsv1.Deployment{}
					err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, deployment)
					require.NoError(t, err, "failed to convert Deployment %s", tc.name)
					break
				}
			}
			require.NotNil(t, deployment, "deployment %s not found in RHOAI overlay", tc.name)

			// Build expected selector with component label
			expected := make(map[string]string)
			for k, v := range expectedLabels {
				expected[k] = v
			}
			expected["app.kubernetes.io/component"] = tc.component

			// Verify selector labels
			assert.Equal(t, expected, deployment.Spec.Selector.MatchLabels,
				"deployment %s selector labels must match RHOAI 3.5 for upgrade compatibility", tc.name)

			// Verify pod template labels (must be superset of selector)
			for key, expectedValue := range expected {
				actualValue, exists := deployment.Spec.Template.Labels[key]
				assert.True(t, exists, "pod template missing selector label %s", key)
				assert.Equal(t, expectedValue, actualValue, "pod template label %s value mismatch", key)
			}
		})
	}
}

// TestODHOverlayPreserves34SelectorLabels verifies that the ODH overlay
// preserves the 6 selector labels that ODH 3.4/3.5 created.
func TestODHOverlayPreserves34SelectorLabels(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	overlayDir := filepath.Join(repoRoot, "config", "overlays", "odh")
	resources := buildOverlay(t, overlayDir)

	// ODH 3.4/3.5 component created these selector labels
	expectedLabels := map[string]string{
		"app.kubernetes.io/name":           "spark-operator",
		"app.kubernetes.io/instance":       "spark-operator",
		"app.kubernetes.io/managed-by":     "kustomize",
		"app.kubernetes.io/part-of":        "sparkoperator",
		"app.opendatahub.io/sparkoperator": "true",
	}

	testCases := []struct {
		name      string
		component string
	}{
		{"spark-operator-controller", "controller"},
		{"spark-operator-webhook", "webhook"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var deployment *appsv1.Deployment
			for _, obj := range resources {
				if obj.GetKind() == "Deployment" && obj.GetName() == tc.name {
					deployment = &appsv1.Deployment{}
					err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, deployment)
					require.NoError(t, err, "failed to convert Deployment %s", tc.name)
					break
				}
			}
			require.NotNil(t, deployment, "deployment %s not found in ODH overlay", tc.name)

			expected := make(map[string]string)
			for k, v := range expectedLabels {
				expected[k] = v
			}
			expected["app.kubernetes.io/component"] = tc.component

			assert.Equal(t, expected, deployment.Spec.Selector.MatchLabels,
				"deployment %s selector labels must match ODH 3.4/3.5 for upgrade compatibility", tc.name)

			for key, expectedValue := range expected {
				actualValue, exists := deployment.Spec.Template.Labels[key]
				assert.True(t, exists, "pod template missing selector label %s", key)
				assert.Equal(t, expectedValue, actualValue, "pod template label %s value mismatch", key)
			}
		})
	}
}

// TestNetworkPolicySelectorsNotPolluted ensures that NetworkPolicy podSelectors
// do NOT contain deployment-specific labels. This is a regression test for the
// initial fix attempt that used includeSelectors: true, which would have broken
// Spark job networking by requiring Spark pods to have labels they don't carry.
func TestNetworkPolicySelectorsNotPolluted(t *testing.T) {
	// Deployment-specific labels that should NOT appear in NetworkPolicy selectors
	pollutingLabels := []string{
		"app.kubernetes.io/part-of",
		"app.opendatahub.io/sparkoperator",
	}

	overlays := map[string]string{
		"rhoai": "config/overlays/rhoai",
		"odh":   "config/overlays/odh",
	}

	repoRoot := filepath.Join("..", "..")

	for overlayName, overlayPath := range overlays {
		t.Run(overlayName, func(t *testing.T) {
			overlayDir := filepath.Join(repoRoot, overlayPath)
			resources := buildOverlay(t, overlayDir)

			// Extract all NetworkPolicy resources
			for _, obj := range resources {
				if obj.GetKind() != "NetworkPolicy" {
					continue
				}

				var policy networkingv1.NetworkPolicy
				err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &policy)
				require.NoError(t, err, "failed to convert NetworkPolicy %s", obj.GetName())

				podSelector := policy.Spec.PodSelector.MatchLabels

				for _, label := range pollutingLabels {
					_, exists := podSelector[label]
					assert.False(t, exists,
						"NetworkPolicy %s podSelector must not have deployment label %s "+
							"(would break Spark jobs by requiring Spark pods to have labels they don't carry)",
						policy.Name, label)
				}
			}
		})
	}
}
