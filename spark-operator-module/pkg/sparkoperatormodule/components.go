package sparkoperatormodule

import (
	"context"
	"fmt"
	"path/filepath"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"

	odhLabels "github.com/opendatahub-io/odh-platform-utilities/pkg/metadata/labels"

	platformv1alpha1 "github.com/opendatahub-io/spark-operator-module/pkg/apis/v1alpha1"
)

type componentConfig struct {
	name       string
	sourcePath string
	imageMap   map[string]string
}

func (r *SparkOperatorModuleReconciler) getComponentConfig(ctx context.Context) componentConfig {
	return componentConfig{
		name:       SparkOperatorComponentName,
		sourcePath: r.getManifestSourcePath(ctx),
		imageMap:   sparkOperatorImageParamMap,
	}
}

func (r *SparkOperatorModuleReconciler) reconcileComponent(ctx context.Context,
	_ *platformv1alpha1.SparkOperator, manifestDir string, comp componentConfig) ([]unstructured.Unstructured, error) {

	log := ctrl.LoggerFrom(ctx)

	renderPath := filepath.Join(manifestDir, comp.name, comp.sourcePath)
	if err := applyParams(renderPath, comp.imageMap); err != nil {
		return nil, fmt.Errorf("applying %s image params: %w", comp.name, err)
	}

	resources, err := renderKustomize(renderPath, r.getApplicationsNamespace(ctx))
	if err != nil {
		return nil, fmt.Errorf("rendering %s kustomize: %w", comp.name, err)
	}

	applyManagedByLabel(resources, SparkOperatorComponentName)
	log.Info("rendered kustomize manifests", "component", comp.name, "resourceCount", len(resources))

	return resources, nil
}

func applyManagedByLabel(resources []unstructured.Unstructured, componentName string) {
	for i := range resources {
		labels := resources[i].GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		labels[odhLabels.PlatformPartOf] = componentName
		resources[i].SetLabels(labels)
	}
}
