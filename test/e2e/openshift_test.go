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

package e2e_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubeflow/spark-operator/v2/api/v1beta2"
)

var _ = Describe("OpenShift Integration Tests", func() {
	Context("docling-spark-openshift-example", func() {
		ctx := context.Background()
		path := filepath.Join("..", "..", "examples", "openshift", "k8s", "docling-spark-app.yaml")
		app := &v1beta2.SparkApplication{}
		
		var (
			testNamespace    *corev1.Namespace
			serviceAccount   *corev1.ServiceAccount
			clusterRole      *rbacv1.ClusterRole
			clusterRoleBinding *rbacv1.ClusterRoleBinding
		)

		BeforeEach(func() {
			By("Creating the docling-spark namespace")
			testNamespace = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "docling-spark",
					Labels: map[string]string{
						"test": "openshift-integration",
					},
				},
			}
			Expect(k8sClient.Create(ctx, testNamespace)).To(Succeed())

			By("Creating RBAC resources for OpenShift")
			// Create ServiceAccount
			serviceAccount = &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "spark-driver",
					Namespace: "docling-spark",
					Labels: map[string]string{
						"test": "openshift-integration",
					},
				},
			}
			Expect(k8sClient.Create(ctx, serviceAccount)).To(Succeed())

			// Create ClusterRole with necessary permissions
			clusterRole = &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{
					Name: "docling-spark-driver-role",
					Labels: map[string]string{
						"test": "openshift-integration",
					},
				},
				Rules: []rbacv1.PolicyRule{
					{
						APIGroups: [""],
						Resources: ["pods", "services", "configmaps", "persistentvolumeclaims"],
						Verbs:     ["create", "get", "list", "watch", "delete", "update", "patch"],
					},
					{
						APIGroups: [""],
						Resources: ["events"],
						Verbs:     ["create", "get", "list", "watch"],
					},
				},
			}
			Expect(k8sClient.Create(ctx, clusterRole)).To(Succeed())

			// Create ClusterRoleBinding
			clusterRoleBinding = &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "docling-spark-driver-binding",
					Labels: map[string]string{
						"test": "openshift-integration",
					},
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:      "ServiceAccount",
						Name:      "spark-driver",
						Namespace: "docling-spark",
					},
				},
				RoleRef: rbacv1.RoleRef{
					Kind:     "ClusterRole",
					Name:     "docling-spark-driver-role",
					APIGroup: "rbac.authorization.k8s.io",
				},
			}
			Expect(k8sClient.Create(ctx, clusterRoleBinding)).To(Succeed())

			By("Parsing SparkApplication from OpenShift example")
			file, err := os.Open(path)
			Expect(err).NotTo(HaveOccurred())
			defer file.Close()

			decoder := yaml.NewYAMLOrJSONDecoder(file, 100)
			Expect(decoder.Decode(app)).NotTo(HaveOccurred())

			By("Creating SparkApplication")
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
		})

		AfterEach(func() {
			By("Cleaning up SparkApplication")
			if app != nil {
				key := types.NamespacedName{Namespace: app.Namespace, Name: app.Name}
				currentApp := &v1beta2.SparkApplication{}
				if err := k8sClient.Get(ctx, key, currentApp); err == nil {
					Expect(k8sClient.Delete(ctx, currentApp)).To(Succeed())
				}
			}

			By("Cleaning up RBAC resources")
			if clusterRoleBinding != nil {
				Expect(k8sClient.Delete(ctx, clusterRoleBinding)).To(Succeed())
			}
			if clusterRole != nil {
				Expect(k8sClient.Delete(ctx, clusterRole)).To(Succeed())
			}
			if serviceAccount != nil {
				Expect(k8sClient.Delete(ctx, serviceAccount)).To(Succeed())
			}

			By("Cleaning up namespace")
			if testNamespace != nil {
				Expect(k8sClient.Delete(ctx, testNamespace)).To(Succeed())
			}
		})

		It("Should validate OpenShift security context constraints compliance", func() {
			By("Verifying restricted-v2 SCC compliance in SparkApplication spec")
			
			// Verify application type and basic config
			Expect(app.Spec.Type).To(Equal(v1beta2.PythonApplicationType))
			Expect(app.Spec.Mode).To(Equal(v1beta2.ClusterMode))
			Expect(app.Spec.Image).To(Equal("quay.io/rishasin/docling-spark:latest"))
			Expect(app.Spec.ImagePullPolicy).To(Equal(corev1.PullAlways))

			// Verify OpenShift-compatible driver security context
			Expect(app.Spec.Driver.SecurityContext).NotTo(BeNil())
			driverSecCtx := app.Spec.Driver.SecurityContext
			
			// restricted-v2 SCC requirements
			Expect(*driverSecCtx.RunAsNonRoot).To(BeTrue(), "Driver must run as non-root for OpenShift restricted-v2 SCC")
			Expect(*driverSecCtx.AllowPrivilegeEscalation).To(BeFalse(), "Driver must not allow privilege escalation")
			Expect(driverSecCtx.Capabilities.Drop).To(ContainElement(corev1.Capability("ALL")), "Driver must drop all capabilities")
			Expect(driverSecCtx.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault), "Driver must use RuntimeDefault seccomp profile")
			
			// OpenShift assigns UIDs, so these should be nil
			Expect(driverSecCtx.RunAsUser).To(BeNil(), "Driver runAsUser should be nil to let OpenShift assign UID")
			Expect(driverSecCtx.RunAsGroup).To(BeNil(), "Driver runAsGroup should be nil to let OpenShift assign GID")

			// Verify OpenShift-compatible executor security context
			Expect(app.Spec.Executor.SecurityContext).NotTo(BeNil())
			executorSecCtx := app.Spec.Executor.SecurityContext
			
			// Same requirements for executors
			Expect(*executorSecCtx.RunAsNonRoot).To(BeTrue(), "Executor must run as non-root for OpenShift restricted-v2 SCC")
			Expect(*executorSecCtx.AllowPrivilegeEscalation).To(BeFalse(), "Executor must not allow privilege escalation")
			Expect(executorSecCtx.Capabilities.Drop).To(ContainElement(corev1.Capability("ALL")), "Executor must drop all capabilities")
			Expect(executorSecCtx.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault), "Executor must use RuntimeDefault seccomp profile")
			
			Expect(executorSecCtx.RunAsUser).To(BeNil(), "Executor runAsUser should be nil to let OpenShift assign UID")
			Expect(executorSecCtx.RunAsGroup).To(BeNil(), "Executor runAsGroup should be nil to let OpenShift assign GID")

			By("Verifying OpenShift-specific resource configuration")
			// Verify service account
			Expect(app.Spec.Driver.ServiceAccount).NotTo(BeNil())
			Expect(*app.Spec.Driver.ServiceAccount).To(Equal("spark-driver"))

			// Verify resource limits suitable for OpenShift
			Expect(*app.Spec.Driver.Cores).To(Equal(int32(1)))
			Expect(*app.Spec.Driver.CoreLimit).To(Equal("1200m"))
			Expect(*app.Spec.Driver.Memory).To(Equal("4g"))
			
			Expect(*app.Spec.Executor.Instances).To(Equal(int32(2)))
			Expect(*app.Spec.Executor.Cores).To(Equal(int32(1)))
			Expect(*app.Spec.Executor.Memory).To(Equal("4g"))
		})

		It("Should successfully submit and create pods with OpenShift security constraints", func() {
			By("Waiting for SparkApplication to be submitted by the operator")
			key := types.NamespacedName{Namespace: app.Namespace, Name: app.Name}
			
			Eventually(func() v1beta2.ApplicationStateType {
				currentApp := &v1beta2.SparkApplication{}
				err := k8sClient.Get(ctx, key, currentApp)
				if err != nil {
					return ""
				}
				return currentApp.Status.AppState.State
			}, 3*time.Minute, 10*time.Second).Should(Equal(v1beta2.ApplicationStateSubmitted))

			By("Verifying driver pod creation with OpenShift-compliant security context")
			var driverPod corev1.Pod
			Eventually(func() bool {
				pods := &corev1.PodList{}
				listOpts := []client.ListOption{
					client.InNamespace("docling-spark"),
					client.MatchingLabels{"spark-role": "driver"},
				}
				
				err := k8sClient.List(ctx, pods, listOpts...)
				if err != nil || len(pods.Items) == 0 {
					return false
				}
				
				driverPod = pods.Items[0]
				return true
			}, 3*time.Minute, 10*time.Second).Should(BeTrue())

			// Verify pod-level security context
			Expect(driverPod.Spec.SecurityContext).NotTo(BeNil())
			Expect(*driverPod.Spec.SecurityContext.RunAsNonRoot).To(BeTrue())
			Expect(driverPod.Spec.SecurityContext.SeccompProfile).NotTo(BeNil())
			Expect(driverPod.Spec.SecurityContext.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))

			// Verify container-level security context
			found := false
			for _, container := range driverPod.Spec.Containers {
				if container.Name == "spark-kubernetes-driver" {
					found = true
					Expect(container.SecurityContext).NotTo(BeNil())
					Expect(*container.SecurityContext.RunAsNonRoot).To(BeTrue())
					Expect(*container.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
					Expect(container.SecurityContext.Capabilities.Drop).To(ContainElement(corev1.Capability("ALL")))
					break
				}
			}
			Expect(found).To(BeTrue(), "Should find spark-kubernetes-driver container")

			By("Waiting for executor pods to be created")
			Eventually(func() int {
				pods := &corev1.PodList{}
				listOpts := []client.ListOption{
					client.InNamespace("docling-spark"),
					client.MatchingLabels{"spark-role": "executor"},
				}
				
				err := k8sClient.List(ctx, pods, listOpts...)
				if err != nil {
					return 0
				}
				return len(pods.Items)
			}, 5*time.Minute, 15*time.Second).Should(BeNumerically(">=", 1)) // At least 1 executor should be created

			By("Verifying executor pods have OpenShift-compliant security context")
			pods := &corev1.PodList{}
			listOpts := []client.ListOption{
				client.InNamespace("docling-spark"),
				client.MatchingLabels{"spark-role": "executor"},
			}
			Expect(k8sClient.List(ctx, pods, listOpts...)).To(Succeed())

			for _, pod := range pods.Items {
				// Verify pod-level security context
				Expect(pod.Spec.SecurityContext).NotTo(BeNil())
				Expect(*pod.Spec.SecurityContext.RunAsNonRoot).To(BeTrue())
				Expect(pod.Spec.SecurityContext.SeccompProfile).NotTo(BeNil())
				Expect(pod.Spec.SecurityContext.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))

				// Verify container security context
				for _, container := range pod.Spec.Containers {
					if container.Name == "executor" {
						Expect(container.SecurityContext).NotTo(BeNil())
						Expect(*container.SecurityContext.RunAsNonRoot).To(BeTrue())
						Expect(*container.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
						Expect(container.SecurityContext.Capabilities.Drop).To(ContainElement(corev1.Capability("ALL")))
					}
				}
			}
		})

		It("Should handle Python application configuration correctly", func() {
			By("Verifying Python-specific configuration is preserved")
			
			// Verify Python application settings
			Expect(app.Spec.Type).To(Equal(v1beta2.PythonApplicationType))
			if app.Spec.PythonVersion != nil {
				Expect(*app.Spec.PythonVersion).To(Equal("3"))
			}
			
			// Verify main application file
			Expect(app.Spec.MainApplicationFile).To(Equal("local:///app/scripts/run_spark_job.py"))
			
			// Verify command-line arguments
			expectedArgs := []string{
				"--input-dir", "/app/assets",
				"--output-file", "/app/output/results.jsonl",
			}
			Expect(app.Spec.Arguments).To(Equal(expectedArgs))

			// Verify Spark version compatibility
			Expect(app.Spec.SparkVersion).To(Equal("3.5.0"))

			// Verify restart policy
			Expect(app.Spec.RestartPolicy.Type).To(Equal(v1beta2.Never))

			// Verify TTL configuration
			Expect(app.Spec.TimeToLiveSeconds).NotTo(BeNil())
			Expect(*app.Spec.TimeToLiveSeconds).To(Equal(int64(1200))) // 20 minutes
		})

		It("Should validate OpenShift namespace isolation", func() {
			By("Verifying application is deployed in the correct namespace")
			Expect(app.Namespace).To(Equal("docling-spark"))

			By("Verifying namespace has proper labels")
			namespace := &corev1.Namespace{}
			namespaceKey := types.NamespacedName{Name: "docling-spark"}
			Expect(k8sClient.Get(ctx, namespaceKey, namespace)).To(Succeed())
			Expect(namespace.Labels["test"]).To(Equal("openshift-integration"))

			By("Verifying ServiceAccount exists in the correct namespace")
			sa := &corev1.ServiceAccount{}
			saKey := types.NamespacedName{Name: "spark-driver", Namespace: "docling-spark"}
			Expect(k8sClient.Get(ctx, saKey, sa)).To(Succeed())

			By("Verifying RBAC is properly configured")
			// Verify ClusterRole exists
			cr := &rbacv1.ClusterRole{}
			crKey := types.NamespacedName{Name: "docling-spark-driver-role"}
			Expect(k8sClient.Get(ctx, crKey, cr)).To(Succeed())

			// Verify ClusterRoleBinding exists and references correct ServiceAccount
			crb := &rbacv1.ClusterRoleBinding{}
			crbKey := types.NamespacedName{Name: "docling-spark-driver-binding"}
			Expect(k8sClient.Get(ctx, crbKey, crb)).To(Succeed())
			
			found := false
			for _, subject := range crb.Subjects {
				if subject.Kind == "ServiceAccount" && 
				   subject.Name == "spark-driver" && 
				   subject.Namespace == "docling-spark" {
					found = true
					break
				}
			}
			Expect(found).To(BeTrue(), "ClusterRoleBinding should reference the spark-driver ServiceAccount")
		})
	})
})