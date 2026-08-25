package sparkoperatormodule_test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"

	platformv1alpha1 "github.com/opendatahub-io/spark-operator-module/pkg/apis/v1alpha1"
	"github.com/opendatahub-io/spark-operator-module/pkg/sparkoperatormodule"
	"github.com/opendatahub-io/spark-operator-module/pkg/sparkoperatormodule/fixture"
)

func TestReconcilerIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SparkOperatorModule Reconciler Integration Suite")
}

var testEnv *fixture.TestEnv

var _ = BeforeSuite(func() {
	testEnv = fixture.SetupTestEnv(context.Background())
})

var _ = Describe("SparkOperatorModule Reconciler", func() {

	It("rejects a SparkOperator CR with wrong name", func(ctx SpecContext) {
		cr := fixture.SparkOperatorCR(fixture.WithName("wrong-name"))
		err := testEnv.Client.Create(ctx, cr)
		Expect(err).To(HaveOccurred())
		Expect(k8serr.IsInvalid(err)).To(BeTrue())
	})

	It("sets error status when manifests are missing", func(ctx SpecContext) {
		savedWorkDir := testEnv.Reconciler.WorkDir()
		testEnv.Reconciler.SetWorkDir(GinkgoT().TempDir())
		DeferCleanup(func() {
			testEnv.Reconciler.SetWorkDir(savedWorkDir)
		})

		cr := fixture.SparkOperatorCR()
		Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) {
			Expect(client.IgnoreNotFound(testEnv.Client.Delete(ctx, cr))).To(Succeed())
		})

		Eventually(func(g Gomega) {
			g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
			cond := fixture.FindCondition(cr, string(common.ConditionTypeProvisioningSucceeded))
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(cr.Status.Phase).To(Equal(common.PhaseNotReady))
			g.Expect(cr.Status.ObservedGeneration).To(Equal(cr.Generation))
		}).WithContext(ctx).Should(Succeed())
	})

	Context("reconcile lifecycle", Ordered, func() {
		var cr *platformv1alpha1.SparkOperator

		BeforeAll(func(ctx SpecContext) {
			cr = fixture.SparkOperatorCR()
			Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())

			DeferCleanup(func(ctx SpecContext) {
				Expect(client.IgnoreNotFound(testEnv.Client.Delete(ctx, cr))).To(Succeed())
			})
		})

		BeforeEach(func() {
			testEnv.Deployer.Reset()
			testEnv.Reconciler.Deployer = testEnv.Deployer
		})

		It("sets provisioning succeeded after successful reconcile", func(ctx SpecContext) {
			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, string(common.ConditionTypeProvisioningSucceeded))
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			}).WithContext(ctx).Should(Succeed())

			lastCall := testEnv.Deployer.LastCall()
			Expect(lastCall).NotTo(BeNil())
			Expect(lastCall.Resources).NotTo(BeEmpty())

			hasConfigMap := false
			for _, res := range lastCall.Resources {
				if res.GetKind() == "ConfigMap" && res.GetName() == "spark-operator-test-config" {
					hasConfigMap = true
					break
				}
			}
			Expect(hasConfigMap).To(BeTrue())
		})

		It("reports ready when workload deployments are available", func(ctx SpecContext) {
			fixture.CreateReadyDeployment(ctx, testEnv.Client, "spark-operator-controller", "opendatahub")
			fixture.CreateReadyDeployment(ctx, testEnv.Client, "spark-operator-webhook", "opendatahub")

			fixture.TriggerReconcile(ctx, testEnv.Client, cr, "readiness")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				g.Expect(cr.Status.Phase).To(Equal(common.PhaseReady))

				ready := fixture.FindCondition(cr, string(common.ConditionTypeReady))
				g.Expect(ready).NotTo(BeNil())
				g.Expect(ready.Status).To(Equal(metav1.ConditionTrue))

				sparkReady := fixture.FindCondition(cr, sparkoperatormodule.ConditionSparkOperatorReady)
				g.Expect(sparkReady).NotTo(BeNil())
				g.Expect(sparkReady.Status).To(Equal(metav1.ConditionTrue))
			}).WithContext(ctx).Should(Succeed())
		})
	})

	It("handles managementState Removed", func(ctx SpecContext) {
		cr := fixture.SparkOperatorCR(fixture.WithManagementState(common.Removed))
		Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) {
			Expect(client.IgnoreNotFound(testEnv.Client.Delete(ctx, cr))).To(Succeed())
		})

		Eventually(func(g Gomega) {
			g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
			cond := fixture.FindCondition(cr, string(common.ConditionTypeProvisioningSucceeded))
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		}).WithContext(ctx).Should(Succeed())
	})

	Context("readiness transitions and status completeness", Ordered, func() {
		var cr *platformv1alpha1.SparkOperator

		BeforeAll(func(ctx SpecContext) {
			cr = fixture.SparkOperatorCR()
			Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())

			DeferCleanup(func(ctx SpecContext) {
				Expect(client.IgnoreNotFound(testEnv.Client.Delete(ctx, cr))).To(Succeed())
			})
		})

		BeforeEach(func() {
			testEnv.Deployer.Reset()
			testEnv.Reconciler.Deployer = testEnv.Deployer
		})

		It("sets ObservedGeneration and populates releases on success path", func(ctx SpecContext) {
			fixture.CreateReadyDeployment(ctx, testEnv.Client, "spark-operator-controller", "opendatahub")
			fixture.CreateReadyDeployment(ctx, testEnv.Client, "spark-operator-webhook", "opendatahub")
			fixture.TriggerReconcile(ctx, testEnv.Client, cr, "releases-check")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				g.Expect(cr.Status.Phase).To(Equal(common.PhaseReady))
				g.Expect(cr.Status.ObservedGeneration).To(Equal(cr.Generation))
				g.Expect(cr.Status.Releases).NotTo(BeEmpty())
				g.Expect(cr.Status.Releases[0].Name).To(Equal(fixture.TestReleaseName))
				g.Expect(cr.Status.Releases[0].Version).To(Equal(fixture.TestReleaseVersion))
			}).WithContext(ctx).Should(Succeed())
		})

		It("transitions Ready from True to False when a deployment becomes unavailable", func(ctx SpecContext) {
			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				ready := fixture.FindCondition(cr, string(common.ConditionTypeReady))
				g.Expect(ready).NotTo(BeNil())
				g.Expect(ready.Status).To(Equal(metav1.ConditionTrue))
			}).WithContext(ctx).Should(Succeed())

			webhookDep := fixture.ReadyDeployment("spark-operator-webhook", "opendatahub")
			Expect(testEnv.Client.Delete(ctx, webhookDep)).To(Succeed())

			fixture.TriggerReconcile(ctx, testEnv.Client, cr, "degrade-webhook")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())

				sparkReady := fixture.FindCondition(cr, sparkoperatormodule.ConditionSparkOperatorReady)
				g.Expect(sparkReady).NotTo(BeNil())
				g.Expect(sparkReady.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(sparkReady.Reason).To(Equal("DeploymentNotReady"))

				g.Expect(cr.Status.Phase).To(Equal(common.PhaseNotReady))
			}).WithContext(ctx).Should(Succeed())
		})

		It("recovers to Ready when deployment is restored", func(ctx SpecContext) {
			fixture.CreateReadyDeployment(ctx, testEnv.Client, "spark-operator-webhook", "opendatahub")
			fixture.TriggerReconcile(ctx, testEnv.Client, cr, "recover-webhook")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				g.Expect(cr.Status.Phase).To(Equal(common.PhaseReady))

				sparkReady := fixture.FindCondition(cr, sparkoperatormodule.ConditionSparkOperatorReady)
				g.Expect(sparkReady).NotTo(BeNil())
				g.Expect(sparkReady.Status).To(Equal(metav1.ConditionTrue))
			}).WithContext(ctx).Should(Succeed())
		})

		It("does not overwrite platform-managed spec fields", func(ctx SpecContext) {
			fixture.TriggerReconcile(ctx, testEnv.Client, cr, "spec-invariant")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				g.Expect(cr.Status.Phase).To(Equal(common.PhaseReady))
			}).WithContext(ctx).Should(Succeed())

			latest := &platformv1alpha1.SparkOperator{}
			Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), latest)).To(Succeed())
			Expect(latest.Spec.ManagementState).To(Equal(common.Managed))
		})
	})

	Context("spec mutation propagation", func() {
		It("transitions from Managed to Removed and back to Managed", func(ctx SpecContext) {
			cr := fixture.SparkOperatorCR()
			Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())
			DeferCleanup(func(ctx SpecContext) {
				Expect(client.IgnoreNotFound(testEnv.Client.Delete(ctx, cr))).To(Succeed())
			})

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, string(common.ConditionTypeProvisioningSucceeded))
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			}).WithContext(ctx).Should(Succeed())

			Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
			cr.Spec.ManagementState = common.Removed
			Expect(testEnv.Client.Update(ctx, cr)).To(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				sparkReady := fixture.FindCondition(cr, sparkoperatormodule.ConditionSparkOperatorReady)
				g.Expect(sparkReady).To(BeNil())
			}).WithContext(ctx).Should(Succeed())

			Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
			cr.Spec.ManagementState = common.Managed
			Expect(testEnv.Client.Update(ctx, cr)).To(Succeed())

			testEnv.Deployer.Reset()

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, string(common.ConditionTypeProvisioningSucceeded))
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))

				lastCall := testEnv.Deployer.LastCall()
				g.Expect(lastCall).NotTo(BeNil())
				g.Expect(lastCall.Resources).NotTo(BeEmpty())
			}).WithContext(ctx).Should(Succeed())
		})
	})
})
