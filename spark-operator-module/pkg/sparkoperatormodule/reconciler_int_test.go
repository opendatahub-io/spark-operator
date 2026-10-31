package sparkoperatormodule_test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
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

	It("reconciles operands but withholds the handshake when platform config is missing", func(ctx SpecContext) {
		config := &corev1.ConfigMap{}
		configKey := client.ObjectKey{Name: fixture.TestPlatformConfigMapName, Namespace: "opendatahub"}
		Expect(testEnv.Client.Get(ctx, configKey, config)).To(Succeed())
		Expect(testEnv.Client.Delete(ctx, config)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) {
			Expect(testEnv.Client.Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: fixture.TestPlatformConfigMapName, Namespace: "opendatahub"},
				Data:       map[string]string{fixture.TestPlatformVersionKey: fixture.TestPlatformVersion},
			})).To(Succeed())
		})

		fixture.CreateReadyDeployment(ctx, testEnv.Client, "spark-operator-controller", "opendatahub")
		fixture.CreateReadyDeployment(ctx, testEnv.Client, "spark-operator-webhook", "opendatahub")
		DeferCleanup(func(ctx SpecContext) {
			Expect(client.IgnoreNotFound(testEnv.Client.Delete(ctx,
				fixture.ReadyDeployment("spark-operator-controller", "opendatahub")))).To(Succeed())
			Expect(client.IgnoreNotFound(testEnv.Client.Delete(ctx,
				fixture.ReadyDeployment("spark-operator-webhook", "opendatahub")))).To(Succeed())
		})

		testEnv.Deployer.Reset()
		cr := fixture.SparkOperatorCR()
		Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) {
			Expect(client.IgnoreNotFound(testEnv.Client.Delete(ctx, cr))).To(Succeed())
		})

		Eventually(func(g Gomega) {
			g.Expect(testEnv.Deployer.LastCall()).NotTo(BeNil())
			g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
			provisioning := fixture.FindCondition(cr, string(common.ConditionTypeProvisioningSucceeded))
			g.Expect(provisioning).NotTo(BeNil())
			g.Expect(provisioning.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(provisioning.Reason).To(Equal("PlatformVersionUnavailable"))
			g.Expect(cr.Status.Phase).To(Equal(common.PhaseNotReady))
			g.Expect(fixture.FindRelease(cr, fixture.TestPlatformReleaseName)).To(BeNil())
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

	It("applies spark.jobNamespaces to webhook namespaceSelectors", func(ctx SpecContext) {
		cr := fixture.SparkOperatorCR(fixture.WithJobNamespaces("default", "spark-bench-a", "spark-bench-b"))
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

		lastCall := testEnv.Deployer.LastCall()
		Expect(lastCall).NotTo(BeNil())

		want := []any{"default", "spark-bench-a", "spark-bench-b"}
		foundWebhook := false
		for _, res := range lastCall.Resources {
			kind := res.GetKind()
			if kind != "MutatingWebhookConfiguration" && kind != "ValidatingWebhookConfiguration" {
				continue
			}
			foundWebhook = true
			webhooks, found, err := unstructured.NestedSlice(res.Object, "webhooks")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			for _, raw := range webhooks {
				webhook := raw.(map[string]any)
				selector := webhook["namespaceSelector"].(map[string]any)
				exprs := selector["matchExpressions"].([]any)
				Expect(exprs).To(HaveLen(1))
				expr := exprs[0].(map[string]any)
				Expect(expr["key"]).To(Equal("kubernetes.io/metadata.name"))
				Expect(expr["operator"]).To(Equal("In"))
				Expect(expr["values"]).To(Equal(want))
			}
		}
		Expect(foundWebhook).To(BeTrue())
	})

	It("applies spark.controllerResources to the controller Deployment", func(ctx SpecContext) {
		cr := fixture.SparkOperatorCR(fixture.WithControllerResources(corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("2Gi"),
				corev1.ResourceCPU:    resource.MustParse("2"),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("512Mi"),
				corev1.ResourceCPU:    resource.MustParse("200m"),
			},
		}))
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

		lastCall := testEnv.Deployer.LastCall()
		Expect(lastCall).NotTo(BeNil())

		found := false
		for _, res := range lastCall.Resources {
			if res.GetKind() != "Deployment" || res.GetName() != "spark-operator-controller" {
				continue
			}
			found = true
			containers, ok, err := unstructured.NestedSlice(res.Object, "spec", "template", "spec", "containers")
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(containers).NotTo(BeEmpty())
			resourcesObj := containers[0].(map[string]any)["resources"].(map[string]any)
			Expect(resourcesObj["limits"]).To(HaveKeyWithValue("memory", "2Gi"))
			Expect(resourcesObj["limits"]).To(HaveKeyWithValue("cpu", "2"))
			Expect(resourcesObj["requests"]).To(HaveKeyWithValue("memory", "512Mi"))
			Expect(resourcesObj["requests"]).To(HaveKeyWithValue("cpu", "200m"))
		}
		Expect(found).To(BeTrue())
	})

	It("applies spark.controllerConfiguration to the controller Deployment", func(ctx SpecContext) {
		workers, workqueueQPS, workqueueBurst := int32(20), int32(100), int32(500)
		kubeQPS, kubeBurst := int32(50), int32(100)
		maxDelay := "6h"
		maxDelayEnabled := true
		cr := fixture.SparkOperatorCR(fixture.WithControllerConfiguration(platformv1alpha1.ControllerConfiguration{
			Workers: &workers,
			WorkqueueRateLimiter: &platformv1alpha1.WorkqueueRateLimiterConfiguration{
				BucketQPS:  &workqueueQPS,
				BucketSize: &workqueueBurst,
				MaxDelay: &platformv1alpha1.WorkqueueMaxDelayConfiguration{
					Enabled:  &maxDelayEnabled,
					Duration: &maxDelay,
				},
			},
			KubeAPIQPS:   &kubeQPS,
			KubeAPIBurst: &kubeBurst,
		}))
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

		lastCall := testEnv.Deployer.LastCall()
		Expect(lastCall).NotTo(BeNil())

		found := false
		for _, res := range lastCall.Resources {
			if res.GetKind() != "Deployment" || res.GetName() != "spark-operator-controller" {
				continue
			}
			found = true
			containers, ok, err := unstructured.NestedSlice(res.Object, "spec", "template", "spec", "containers")
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(containers).NotTo(BeEmpty())
			rawArgs := containers[0].(map[string]any)["args"].([]any)
			Expect(rawArgs).To(ContainElements(
				"--controller-threads=20",
				"--workqueue-ratelimiter-bucket-qps=100",
				"--workqueue-ratelimiter-bucket-size=500",
				"--workqueue-ratelimiter-max-delay=6h",
				"--kube-api-qps=50",
				"--kube-api-burst=100",
			))
		}
		Expect(found).To(BeTrue())
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

				componentRelease := fixture.FindRelease(cr, fixture.TestReleaseName)
				g.Expect(componentRelease).NotTo(BeNil())
				g.Expect(componentRelease.Version).To(Equal(fixture.TestReleaseVersion))

				platformRelease := fixture.FindRelease(cr, fixture.TestPlatformReleaseName)
				g.Expect(platformRelease).NotTo(BeNil())
				g.Expect(platformRelease.Version).To(Equal(fixture.TestPlatformVersion))
			}).WithContext(ctx).Should(Succeed())
		})

		It("updates the platform release when the platform ConfigMap changes", func(ctx SpecContext) {
			config := &corev1.ConfigMap{}
			key := client.ObjectKey{Name: fixture.TestPlatformConfigMapName, Namespace: "opendatahub"}
			Expect(testEnv.Client.Get(ctx, key, config)).To(Succeed())
			config.Data[fixture.TestPlatformVersionKey] = "2.21.0"
			Expect(testEnv.Client.Update(ctx, config)).To(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				platformRelease := fixture.FindRelease(cr, fixture.TestPlatformReleaseName)
				g.Expect(platformRelease).NotTo(BeNil())
				g.Expect(platformRelease.Version).To(Equal("2.21.0"))
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

			config := &corev1.ConfigMap{}
			key := client.ObjectKey{Name: fixture.TestPlatformConfigMapName, Namespace: "opendatahub"}
			Expect(testEnv.Client.Get(ctx, key, config)).To(Succeed())
			config.Data[fixture.TestPlatformVersionKey] = "2.22.0"
			Expect(testEnv.Client.Update(ctx, config)).To(Succeed())
			_, err := testEnv.Reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cr)})
			Expect(err).NotTo(HaveOccurred())

			Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
			platformRelease := fixture.FindRelease(cr, fixture.TestPlatformReleaseName)
			Expect(platformRelease).NotTo(BeNil())
			Expect(platformRelease.Version).To(Equal("2.21.0"))
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

				platformRelease := fixture.FindRelease(cr, fixture.TestPlatformReleaseName)
				g.Expect(platformRelease).NotTo(BeNil())
				g.Expect(platformRelease.Version).To(Equal("2.22.0"))
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
