package sparkoperatormodule

import (
	"context"
	"testing"

	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"
)

func TestParseInjectedPlatform(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw  string
		want cluster.Platform
	}{
		{"", cluster.OpenDataHub},
		{"Open Data Hub", cluster.OpenDataHub},
		{"OpenDataHub", cluster.OpenDataHub},
		{"unknown", cluster.OpenDataHub},
		{"OpenShift AI Self-Managed", cluster.SelfManagedRhoai},
		{"SelfManagedRHOAI", cluster.SelfManagedRhoai},
		{"OpenShift AI Cloud Service", cluster.ManagedRhoai},
		{"ManagedRHOAI", cluster.ManagedRhoai},
		{"XKS", cluster.XKS},
		{"  SelfManagedRHOAI  ", cluster.SelfManagedRhoai},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			t.Parallel()
			if got := parseInjectedPlatform(tt.raw); got != tt.want {
				t.Errorf("parseInjectedPlatform(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestOverlayForPlatform(t *testing.T) {
	t.Parallel()

	tests := []struct {
		p    cluster.Platform
		want string
	}{
		{cluster.OpenDataHub, SparkOperatorManifestSourcePathODH},
		{cluster.XKS, SparkOperatorManifestSourcePathODH},
		{cluster.SelfManagedRhoai, SparkOperatorManifestSourcePathRHOAI},
		{cluster.ManagedRhoai, SparkOperatorManifestSourcePathRHOAI},
	}
	for _, tt := range tests {
		t.Run(string(tt.p), func(t *testing.T) {
			t.Parallel()
			if got := overlayForPlatform(tt.p); got != tt.want {
				t.Errorf("overlayForPlatform(%q) = %q, want %q", tt.p, got, tt.want)
			}
		})
	}
}

func TestGetManifestSourcePathFromInjectedEnv(t *testing.T) {
	t.Setenv(platformTypeEnv, "OpenShift AI Self-Managed")
	t.Setenv(platformTypeEnvLegacy, "OpenDataHub")

	r := &SparkOperatorModuleReconciler{}
	got := r.getManifestSourcePath(context.Background())
	if got != SparkOperatorManifestSourcePathRHOAI {
		t.Errorf("overlay = %q, want RHOAI overlay (injected platform env wins over legacy)", got)
	}
}

func TestGetManifestSourcePathLegacyEnv(t *testing.T) {
	t.Setenv(platformTypeEnv, "")
	t.Setenv(platformTypeEnvLegacy, "ManagedRHOAI")

	r := &SparkOperatorModuleReconciler{}
	got := r.getManifestSourcePath(context.Background())
	if got != SparkOperatorManifestSourcePathRHOAI {
		t.Errorf("overlay = %q, want RHOAI overlay from legacy env", got)
	}
}

func TestGetManifestSourcePathEmptyEnvDefaultsODH(t *testing.T) {
	t.Setenv(platformTypeEnv, "")
	t.Setenv(platformTypeEnvLegacy, "")

	r := &SparkOperatorModuleReconciler{}
	got := r.getManifestSourcePath(context.Background())
	if got != SparkOperatorManifestSourcePathODH {
		t.Errorf("overlay = %q, want ODH overlay when platform env is unset", got)
	}
}

func TestGetManifestSourcePathSetPlatformWins(t *testing.T) {
	t.Setenv(platformTypeEnv, "OpenShift AI Self-Managed")

	r := &SparkOperatorModuleReconciler{}
	r.SetPlatform(cluster.OpenDataHub)
	got := r.getManifestSourcePath(context.Background())
	if got != SparkOperatorManifestSourcePathODH {
		t.Errorf("overlay = %q, want ODH overlay from SetPlatform", got)
	}
}

func TestGetApplicationsNamespaceFromEnv(t *testing.T) {
	t.Setenv(applicationsNamespaceEnv, "redhat-ods-applications")

	r := &SparkOperatorModuleReconciler{}
	got := r.getApplicationsNamespace(context.Background())
	if got != "redhat-ods-applications" {
		t.Errorf("namespace = %q, want injected applications namespace", got)
	}
}

func TestGetApplicationsNamespaceDefault(t *testing.T) {
	t.Setenv(applicationsNamespaceEnv, "")

	r := &SparkOperatorModuleReconciler{}
	got := r.getApplicationsNamespace(context.Background())
	if got != defaultApplicationsNamespace {
		t.Errorf("namespace = %q, want %q", got, defaultApplicationsNamespace)
	}
}
