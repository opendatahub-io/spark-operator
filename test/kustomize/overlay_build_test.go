/*
Copyright 2024 The Kubeflow authors.

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

package kustomize_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// buildOverlay runs "kubectl kustomize" on the given directory and returns
// the parsed Kubernetes resources. The test is skipped when kubectl is absent.
func buildOverlay(t *testing.T, kustomizeDir string) []unstructured.Unstructured {
	t.Helper()

	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skip("kubectl not found in PATH, skipping kustomize build test")
	}

	cmd := exec.Command("kubectl", "kustomize", kustomizeDir)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "kustomize build failed for %s:\n%s", kustomizeDir, string(output))

	var resources []unstructured.Unstructured
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(output), 4096)
	for {
		obj := unstructured.Unstructured{}
		if err := decoder.Decode(&obj); err != nil {
			if err == io.EOF {
				break
			}
			t.Logf("skipping unparseable document: %v", err)
			continue
		}
		if obj.GetKind() != "" {
			resources = append(resources, obj)
		}
	}

	require.NotEmpty(t, resources, "kustomize build produced no resources for %s", kustomizeDir)
	return resources
}

func overlayFindResource(resources []unstructured.Unstructured, kind, name string) *unstructured.Unstructured {
	for i := range resources {
		if resources[i].GetKind() == kind && resources[i].GetName() == name {
			return &resources[i]
		}
	}
	return nil
}

func overlayFindResources(resources []unstructured.Unstructured, kind string) []unstructured.Unstructured {
	var out []unstructured.Unstructured
	for i := range resources {
		if resources[i].GetKind() == kind {
			out = append(out, resources[i])
		}
	}
	return out
}

func overlayConvertTo[T any](t *testing.T, obj *unstructured.Unstructured) *T {
	t.Helper()
	data, err := json.Marshal(obj.Object)
	require.NoError(t, err)
	result := new(T)
	require.NoError(t, json.Unmarshal(data, result))
	return result
}

// TestOverlayBuilds validates that both ODH and RHOAI overlays build
// successfully and produce expected core resources.
func TestOverlayBuilds(t *testing.T) {
	repoRoot := filepath.Join("..", "..")

	overlays := []struct {
		name      string
		path      string
		namespace string
	}{
		{"odh", filepath.Join(repoRoot, "config", "overlays", "odh"), "opendatahub"},
		{"rhoai", filepath.Join(repoRoot, "config", "overlays", "rhoai"), "redhat-ods-applications"},
	}

	for _, overlay := range overlays {
		t.Run(overlay.name, func(t *testing.T) {
			resources := buildOverlay(t, overlay.path)

			t.Run("CoreResources", func(t *testing.T) {
				controllerDep := overlayFindResource(resources, "Deployment", "spark-operator-controller")
				require.NotNil(t, controllerDep, "Deployment/spark-operator-controller not found in %s overlay", overlay.name)

				webhookDep := overlayFindResource(resources, "Deployment", "spark-operator-webhook")
				require.NotNil(t, webhookDep, "Deployment/spark-operator-webhook not found in %s overlay", overlay.name)

				podMonitor := overlayFindResource(resources, "PodMonitor", "spark-operator-podmonitor")
				require.NotNil(t, podMonitor, "PodMonitor/spark-operator-podmonitor not found in %s overlay", overlay.name)
			})

			t.Run("NamespaceOverride", func(t *testing.T) {
				controllerDep := overlayFindResource(resources, "Deployment", "spark-operator-controller")
				require.NotNil(t, controllerDep)
				assert.Equal(t, overlay.namespace, controllerDep.GetNamespace(),
					"Deployment namespace should be overridden to %s", overlay.namespace)
			})

			t.Run("NoNamespaceResource", func(t *testing.T) {
				ns := overlayFindResource(resources, "Namespace", "spark-operator")
				assert.Nil(t, ns, "Namespace/spark-operator should be deleted by overlay (managed externally)")
			})

			t.Run("ImageReplacement", func(t *testing.T) {
				for _, depName := range []string{"spark-operator-controller", "spark-operator-webhook"} {
					depObj := overlayFindResource(resources, "Deployment", depName)
					require.NotNil(t, depObj)
					dep := overlayConvertTo[appsv1.Deployment](t, depObj)
					for _, c := range dep.Spec.Template.Spec.Containers {
						assert.NotEmpty(t, c.Image, "container image should not be empty in %s/%s", depName, c.Name)
						assert.NotContains(t, c.Image, "ghcr.io/kubeflow",
							"overlay %s should override upstream image in %s container %s", overlay.name, depName, c.Name)
					}
				}
			})

			t.Run("SparkJobRBAC", func(t *testing.T) {
				sa := overlayFindResource(resources, "ServiceAccount", "spark-operator-spark")
				require.NotNil(t, sa,
					"ServiceAccount/spark-operator-spark not found in %s overlay — Spark driver pods need this to create executors", overlay.name)
				assert.Equal(t, overlay.namespace, sa.GetNamespace(),
					"spark-operator-spark SA should be in %s namespace", overlay.namespace)

				role := overlayFindResource(resources, "Role", "spark-role")
				require.NotNil(t, role,
					"Role/spark-role not found in %s overlay — Spark drivers need permissions to manage pods/configmaps", overlay.name)
				assert.Equal(t, overlay.namespace, role.GetNamespace(),
					"spark-role should be in %s namespace", overlay.namespace)

				typedRole := overlayConvertTo[rbacv1.Role](t, role)
				var hasPods, hasConfigMaps bool
				for _, rule := range typedRole.Rules {
					for _, res := range rule.Resources {
						if res == "pods" {
							hasPods = true
						}
						if res == "configmaps" {
							hasConfigMaps = true
						}
					}
				}
				assert.True(t, hasPods, "spark-role should grant access to pods")
				assert.True(t, hasConfigMaps, "spark-role should grant access to configmaps")

				rb := overlayFindResource(resources, "RoleBinding", "spark-role-binding")
				require.NotNil(t, rb,
					"RoleBinding/spark-role-binding not found in %s overlay", overlay.name)
				assert.Equal(t, overlay.namespace, rb.GetNamespace(),
					"spark-role-binding should be in %s namespace", overlay.namespace)

				typedRB := overlayConvertTo[rbacv1.RoleBinding](t, rb)
				assert.Equal(t, "spark-role", typedRB.RoleRef.Name,
					"RoleBinding should reference spark-role")

				hasSA := false
				for _, subj := range typedRB.Subjects {
					if subj.Kind == "ServiceAccount" && subj.Name == "spark-operator-spark" {
						hasSA = true
						break
					}
				}
				assert.True(t, hasSA, "RoleBinding should bind to spark-operator-spark ServiceAccount")
			})

			t.Run("PrometheusMonitoringLabel", func(t *testing.T) {
				pm := overlayFindResource(resources, "PodMonitor", "spark-operator-podmonitor")
				require.NotNil(t, pm)
				assert.Equal(t, overlay.namespace, pm.GetNamespace(),
					"PodMonitor should be in %s namespace", overlay.namespace)
				labels := pm.GetLabels()
				assert.Equal(t, "true", labels["opendatahub.io/monitoring"],
					"PodMonitor should have monitoring label for observability stack")
			})

			t.Run("NetworkPolicy", func(t *testing.T) {
				nps := overlayFindResources(resources, "NetworkPolicy")
				assert.NotEmpty(t, nps, "overlay %s should include NetworkPolicy", overlay.name)
			})
		})
	}
}
