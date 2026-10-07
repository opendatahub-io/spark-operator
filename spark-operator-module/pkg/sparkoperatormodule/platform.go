package sparkoperatormodule

import (
	"os"
	"strings"

	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"
)

// Env names injected by opendatahub-operator injectModuleEnv. Do not probe OLM
// to recover these; the platform operator already knows the product type.
const (
	applicationsNamespaceEnv     = "APPLICATIONS_NAMESPACE"
	platformTypeEnv              = "ODH_MODULE_OPERATOR_PLATFORM_TYPE"
	platformTypeEnvLegacy        = "ODH_PLATFORM_TYPE"
	defaultApplicationsNamespace = "opendatahub"
)

func overlayForPlatform(p cluster.Platform) string {
	switch p {
	case cluster.ManagedRhoai, cluster.SelfManagedRhoai:
		return SparkOperatorManifestSourcePathRHOAI
	default:
		return SparkOperatorManifestSourcePathODH
	}
}

func injectedPlatformType() string {
	if v := os.Getenv(platformTypeEnv); v != "" {
		return v
	}
	// Legacy DetectPlatform env name. The operator injects platformTypeEnv.
	return os.Getenv(platformTypeEnvLegacy)
}

func parseInjectedPlatform(raw string) cluster.Platform {
	s := strings.TrimSpace(raw)
	switch {
	case equalFoldAny(s, string(cluster.ManagedRhoai), "ManagedRHOAI", "ManagedRhoai"):
		return cluster.ManagedRhoai
	case equalFoldAny(s, string(cluster.SelfManagedRhoai), "SelfManagedRHOAI", "SelfManagedRhoai"):
		return cluster.SelfManagedRhoai
	case equalFoldAny(s, string(cluster.XKS), "XKS"):
		return cluster.XKS
	default:
		return cluster.OpenDataHub
	}
}

func equalFoldAny(s string, candidates ...string) bool {
	for _, c := range candidates {
		if strings.EqualFold(s, c) {
			return true
		}
	}
	return false
}
