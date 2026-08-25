/*
Copyright 2025 The Kubeflow authors.

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

package rhoai_test

import (
	"context"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("ScheduledSparkApplication RBAC Preflight", func() {
	ctx := context.Background()

	controllerSA := os.Getenv("CONTROLLER_SA")
	if controllerSA == "" {
		controllerSA = "spark-operator-controller"
	}

	Context("Verify controller ServiceAccount has required RBAC", func() {
		It("Should have permissions for ScheduledSparkApplication lifecycle", func() {
			By("Finding ClusterRoles bound to the controller SA")
			clusterRoleBindings := &rbacv1.ClusterRoleBindingList{}
			Expect(k8sClient.List(ctx, clusterRoleBindings)).To(Succeed())

			var boundRoleNames []string
			for _, crb := range clusterRoleBindings.Items {
				for _, subject := range crb.Subjects {
					if subject.Kind == "ServiceAccount" &&
						subject.Name == controllerSA &&
						subject.Namespace == ReleaseNamespace {
						boundRoleNames = append(boundRoleNames, crb.RoleRef.Name)
					}
				}
			}
			Expect(boundRoleNames).NotTo(BeEmpty(),
				"no ClusterRoleBindings found for SA %s/%s", ReleaseNamespace, controllerSA)

			By("Verifying required permissions exist in bound ClusterRoles")
			type requiredRule struct {
				apiGroup string
				resource string
				verbs    []string
			}
			required := []requiredRule{
				{"sparkoperator.k8s.io", "scheduledsparkapplications", []string{"get", "list", "watch"}},
				{"sparkoperator.k8s.io", "scheduledsparkapplications/status", []string{"update"}},
				{"sparkoperator.k8s.io", "scheduledsparkapplications/finalizers", []string{"update"}},
				{"sparkoperator.k8s.io", "sparkapplications", []string{"create", "get", "list", "watch", "delete"}},
			}

			for _, roleName := range boundRoleNames {
				role := &rbacv1.ClusterRole{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: roleName}, role); err != nil {
					continue
				}
				for i := range required {
					if hasPermission(role.Rules, required[i].apiGroup, required[i].resource, required[i].verbs) {
						required[i].verbs = nil
					}
				}
			}

			for _, r := range required {
				Expect(r.verbs).To(BeNil(),
					"missing RBAC: %s %s %v for SA %s", r.apiGroup, r.resource, r.verbs, controllerSA)
			}
		})
	})
})

func hasPermission(rules []rbacv1.PolicyRule, apiGroup, resource string, verbs []string) bool {
	for _, rule := range rules {
		groupMatch := containsOrWildcard(rule.APIGroups, apiGroup)
		resourceMatch := containsOrWildcard(rule.Resources, resource)
		if !groupMatch || !resourceMatch {
			continue
		}
		allVerbsFound := true
		for _, verb := range verbs {
			if !containsOrWildcard(rule.Verbs, verb) {
				allVerbsFound = false
				break
			}
		}
		if allVerbsFound {
			return true
		}
	}
	return false
}

func containsOrWildcard(slice []string, item string) bool {
	for _, s := range slice {
		if s == "*" || s == item {
			return true
		}
	}
	return false
}
