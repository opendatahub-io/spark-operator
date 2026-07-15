package e2e_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/spark-operator-module/pkg/apis/v1alpha1"
)

var _ = Describe("Module Operator", func() {
	ctx := context.Background()

	Context("Deployment", func() {
		It("should have the controller deployment available", func() {
			dep := &appsv1.Deployment{}
			key := types.NamespacedName{Name: DeploymentName, Namespace: ModuleNamespace}
			Expect(k8sClient.Get(ctx, key, dep)).To(Succeed())
			Expect(dep.Status.AvailableReplicas).To(BeNumerically(">=", 1))
		})

		It("should have the controller pod running", func() {
			pods := &corev1.PodList{}
			Expect(k8sClient.List(ctx, pods,
				client.InNamespace(ModuleNamespace),
				client.MatchingLabels{"control-plane": DeploymentName},
			)).To(Succeed())

			Expect(pods.Items).NotTo(BeEmpty(), "No controller pods found")

			pod := pods.Items[0]
			Expect(pod.Status.Phase).To(Equal(corev1.PodRunning))
		})

		It("should run the container as non-root", func() {
			pods := &corev1.PodList{}
			Expect(k8sClient.List(ctx, pods,
				client.InNamespace(ModuleNamespace),
				client.MatchingLabels{"control-plane": DeploymentName},
			)).To(Succeed())
			Expect(pods.Items).NotTo(BeEmpty())

			pod := pods.Items[0]

			Expect(pod.Spec.SecurityContext).NotTo(BeNil())
			Expect(pod.Spec.SecurityContext.RunAsNonRoot).NotTo(BeNil())
			Expect(*pod.Spec.SecurityContext.RunAsNonRoot).To(BeTrue())

			managerFound := false
			for _, container := range pod.Spec.Containers {
				if container.Name == "manager" {
					managerFound = true
					Expect(container.SecurityContext).NotTo(BeNil())
					Expect(container.SecurityContext.RunAsNonRoot).NotTo(BeNil())
					Expect(*container.SecurityContext.RunAsNonRoot).To(BeTrue())
					Expect(container.SecurityContext.AllowPrivilegeEscalation).NotTo(BeNil())
					Expect(*container.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
				}
			}
			Expect(managerFound).To(BeTrue(), "manager container not found in pod")
		})
	})

	Context("CRD", func() {
		It("should have the SparkOperator CRD registered", func() {
			crd := &apiextensionsv1.CustomResourceDefinition{}
			key := types.NamespacedName{Name: CRDName}
			Expect(k8sClient.Get(ctx, key, crd)).To(Succeed())
			Expect(crd.Spec.Group).To(Equal("components.platform.opendatahub.io"))
			Expect(crd.Spec.Names.Kind).To(Equal("SparkOperator"))
		})
	})

	Context("SparkOperator CR lifecycle", func() {
		AfterEach(func() {
			By("Deleting SparkOperator CR")
			existing := &v1alpha1.SparkOperator{}
			key := types.NamespacedName{Name: CRName}
			if err := k8sClient.Get(ctx, key, existing); err == nil {
				Expect(k8sClient.Delete(ctx, existing)).To(Succeed())
				Eventually(func() bool {
					return apierrors.IsNotFound(k8sClient.Get(ctx, key, &v1alpha1.SparkOperator{}))
				}).WithTimeout(30 * time.Second).WithPolling(PollInterval).Should(BeTrue(),
					"SparkOperator CR should be fully deleted")
			}
		})

		It("should accept a SparkOperator CR with the singleton name", func() {
			By("Creating SparkOperator CR")
			cr := &v1alpha1.SparkOperator{}
			cr.SetName(CRName)
			cr.Spec = v1alpha1.SparkOperatorSpec{}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())

			By("Verifying the CR exists")
			key := types.NamespacedName{Name: CRName}
			Expect(k8sClient.Get(ctx, key, cr)).To(Succeed())
			Expect(cr.UID).NotTo(BeEmpty())
		})

		It("should have the controller reconcile the CR and create operand resources", func() {
			By("Creating SparkOperator CR")
			cr := &v1alpha1.SparkOperator{}
			cr.SetName(CRName)
			cr.Spec = v1alpha1.SparkOperatorSpec{}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())

			By("Waiting for ProvisioningSucceeded condition to be True")
			key := types.NamespacedName{Name: CRName}
			Eventually(func(g Gomega) {
				updated := &v1alpha1.SparkOperator{}
				g.Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())

				var found bool
				for _, c := range updated.Status.Conditions {
					if c.Type == "ProvisioningSucceeded" {
						found = true
						g.Expect(c.Status).To(Equal(metav1.ConditionTrue),
							"ProvisioningSucceeded should be True, got reason: %s, message: %s", c.Reason, c.Message)
					}
				}
				g.Expect(found).To(BeTrue(), "ProvisioningSucceeded condition not yet set")
			}).WithTimeout(90 * time.Second).WithPolling(PollInterval).Should(Succeed())

			By("Verifying operand deployments were created")
			Eventually(func(g Gomega) {
				deployments := &appsv1.DeploymentList{}
				g.Expect(k8sClient.List(ctx, deployments, client.InNamespace(ModuleNamespace))).To(Succeed())
				deploymentNames := make([]string, 0, len(deployments.Items))
				for _, d := range deployments.Items {
					deploymentNames = append(deploymentNames, d.Name)
				}
				g.Expect(deploymentNames).To(ContainElement("spark-operator-controller"),
					"spark-operator-controller deployment should exist")
				g.Expect(deploymentNames).To(ContainElement("spark-operator-webhook"),
					"spark-operator-webhook deployment should exist")
			}).WithTimeout(90 * time.Second).WithPolling(PollInterval).Should(Succeed())

			By("Verifying operand RBAC resources were created")
			Eventually(func(g Gomega) {
				clusterRoles := &rbacv1.ClusterRoleList{}
				g.Expect(k8sClient.List(ctx, clusterRoles)).To(Succeed())
				clusterRoleNames := make([]string, 0)
				for _, cr := range clusterRoles.Items {
					clusterRoleNames = append(clusterRoleNames, cr.Name)
				}
				g.Expect(clusterRoleNames).To(ContainElement("spark-operator-controller"),
					"spark-operator-controller ClusterRole should exist")
			}).WithTimeout(90 * time.Second).WithPolling(PollInterval).Should(Succeed())

			By("Verifying operand ServiceAccount was created")
			Eventually(func(g Gomega) {
				sa := &corev1.ServiceAccount{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name: "spark-operator-controller", Namespace: ModuleNamespace,
				}, sa)).To(Succeed(), "spark-operator-controller ServiceAccount should exist")
			}).WithTimeout(90 * time.Second).WithPolling(PollInterval).Should(Succeed())
		})
	})
})
