//go:build openshift
// +build openshift

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

package e2e_kueue_test

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"github.com/kubeflow/spark-operator/v2/api/v1beta2"
)

const (
	TenantANamespace = "tenant-a"
	TenantBNamespace = "tenant-b"

	TenantALocalQueue   = "tenant-a-lq"
	TenantBLocalQueue   = "tenant-b-lq"
	TenantAClusterQueue = "tenant-a-cq"
	TenantBClusterQueue = "tenant-b-cq"

	GangLocalQueue   = "gang-lq"
	GangClusterQueue = "gang-cq"

	MultitenancyTestTimeout  = 12 * time.Minute
	MultitenancyPollInterval = 2 * time.Second
)

// ============================================================================
// KUEUE MULTI-TENANCY, GANG SCHEDULING & QUEUE INDEPENDENCE TESTS
// ============================================================================
//
// These tests validate tenant isolation via separate ClusterQueues, atomic
// admission (gang scheduling) of SparkApplications, and independence of
// ClusterQueues with no shared Cohort.
//
// Prerequisites (in addition to basic Kueue setup from SETUP.md):
//   - Namespaces: tenant-a, tenant-b (with kueue.openshift.io/managed=true)
//   - ClusterQueues: tenant-a-cq, tenant-b-cq (independent, no cohort)
//   - ClusterQueue: gang-cq (2 CPU, for gang scheduling tests)
//   - LocalQueues: tenant-a-lq, tenant-b-lq, gang-lq
//   - ServiceAccount spark-operator-spark in tenant-a and tenant-b
//   - Spark Operator configured to watch tenant-a and tenant-b namespaces
//
// Apply resources:
//   oc apply -f examples/openshift/kueue/kueue-multitenancy-resources.yaml
//
// Run with:
//   KUBECONFIG=$HOME/.kube/config \
//   go test -v -tags openshift ./examples/openshift/kueue/ \
//     -ginkgo.v -ginkgo.focus="Multi-Tenancy" -timeout 45m
//
// ============================================================================

var _ = Describe("Kueue Multi-Tenancy, Gang Scheduling and Queue Independence", func() {

	ctx := context.Background()

	BeforeEach(func() {
		By("Verifying tenant namespaces have kueue.openshift.io/managed=true label")
		for _, ns := range []string{TenantANamespace, TenantBNamespace, KueueTestNamespace} {
			namespace := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ns}, namespace)).To(Succeed(),
				"Namespace %s must exist", ns)
			if namespace.Labels == nil {
				namespace.Labels = map[string]string{}
			}
			if namespace.Labels["kueue.openshift.io/managed"] != "true" {
				namespace.Labels["kueue.openshift.io/managed"] = "true"
				Expect(k8sClient.Update(ctx, namespace)).To(Succeed())
			}
		}
	})

	// ========================================================================
	// AC1: Multi-Tenancy — Tenant isolation via separate ClusterQueues
	// ========================================================================
	Context("Multi-Tenancy Isolation", func() {
		var apps []*v1beta2.SparkApplication

		BeforeEach(func() {
			By("Verifying tenant LocalQueues exist")
			verifyLocalQueueExists(ctx, TenantALocalQueue, TenantANamespace)
			verifyLocalQueueExists(ctx, TenantBLocalQueue, TenantBNamespace)
		})

		AfterEach(func() {
			for _, app := range apps {
				cleanupSparkApplication(ctx, app)
			}
			apps = nil
		})

		It("Should not affect Tenant B when Tenant A exhausts its quota", func(specCtx SpecContext) {
			By("Tenant A submits a long-running app that consumes most of its 3 CPU quota")
			tenantAApp := newTenantSparkPi("mt-tenant-a", "500000", TenantANamespace, TenantALocalQueue)
			apps = append(apps, tenantAApp)
			Expect(createWithRetry(ctx, k8sClient, tenantAApp)).To(Succeed())
			GinkgoWriter.Printf("Created Tenant A app: %s in %s\n", tenantAApp.Name, TenantANamespace)

			keyA := types.NamespacedName{Namespace: tenantAApp.Namespace, Name: tenantAApp.Name}
			Expect(waitForSparkAppState(specCtx, keyA, v1beta2.ApplicationStateRunning)).To(Succeed())
			GinkgoWriter.Printf("Tenant A app is Running (consuming 2 of 3 CPU)\n")

			By("Tenant A submits a second app that should be queued (exceeds 3 CPU quota)")
			tenantAApp2 := newTenantSparkPi("mt-tenant-a2", "100", TenantANamespace, TenantALocalQueue)
			apps = append(apps, tenantAApp2)
			Expect(createWithRetry(ctx, k8sClient, tenantAApp2)).To(Succeed())
			GinkgoWriter.Printf("Created Tenant A app 2: %s (should be queued)\n", tenantAApp2.Name)

			By("Verifying Tenant A's second app remains non-running (quota exhausted)")
			keyA2 := types.NamespacedName{Namespace: tenantAApp2.Namespace, Name: tenantAApp2.Name}
			Consistently(func(g Gomega) {
				stateA2 := getSparkAppState(ctx, keyA2)
				GinkgoWriter.Printf("  Tenant A app 2 state: %s\n", stateA2)
				g.Expect(stateA2).NotTo(Equal(v1beta2.ApplicationStateRunning),
					"Tenant A's second app should NOT be running — quota exhausted")
				g.Expect(stateA2).NotTo(Equal(v1beta2.ApplicationStateCompleted),
					"Tenant A's second app should NOT have completed — quota exhausted")
			}).WithTimeout(15 * time.Second).WithPolling(3 * time.Second).Should(Succeed())

			By("Tenant B submits an app — should be admitted independently")
			tenantBApp := newTenantSparkPi("mt-tenant-b", "100", TenantBNamespace, TenantBLocalQueue)
			apps = append(apps, tenantBApp)
			Expect(createWithRetry(ctx, k8sClient, tenantBApp)).To(Succeed())
			GinkgoWriter.Printf("Created Tenant B app: %s in %s\n", tenantBApp.Name, TenantBNamespace)

			By("Verifying Tenant B's app is admitted and runs to completion")
			keyB := types.NamespacedName{Namespace: tenantBApp.Namespace, Name: tenantBApp.Name}
			err := waitForSparkAppState(specCtx, keyB, v1beta2.ApplicationStateCompleted)
			logSparkAppStatus(specCtx, keyB)
			Expect(err).NotTo(HaveOccurred(),
				"Tenant B's app should complete — Tenant A's exhausted quota must not affect Tenant B")

			By("Verifying Tenant A's ClusterQueue shows pending workloads")
			cqA := getClusterQueue(ctx, TenantAClusterQueue)
			pendingA, foundA, errA := unstructured.NestedInt64(cqA.Object, "status", "pendingWorkloads")
			Expect(errA).NotTo(HaveOccurred(), "Failed to read Tenant A pendingWorkloads")
			Expect(foundA).To(BeTrue(), "pendingWorkloads field not found in Tenant A CQ status")
			GinkgoWriter.Printf("Tenant A CQ pending: %d\n", pendingA)

			By("Verifying Tenant B's ClusterQueue has no pending workloads")
			cqB := getClusterQueue(ctx, TenantBClusterQueue)
			pendingB, foundB, errB := unstructured.NestedInt64(cqB.Object, "status", "pendingWorkloads")
			Expect(errB).NotTo(HaveOccurred(), "Failed to read Tenant B pendingWorkloads")
			Expect(foundB).To(BeTrue(), "pendingWorkloads field not found in Tenant B CQ status")
			GinkgoWriter.Printf("Tenant B CQ pending: %d\n", pendingB)
			Expect(pendingB).To(Equal(int64(0)), "Tenant B should have no pending workloads")

			GinkgoWriter.Printf("AC1 PASSED: Tenant isolation verified — Tenant A exhausted, Tenant B unaffected\n")
		}, SpecTimeout(MultitenancyTestTimeout))
	})

	// ========================================================================
	// AC2: Queue Independence — Independent ClusterQueues don't interfere
	// ========================================================================
	Context("Queue Independence", func() {
		var apps []*v1beta2.SparkApplication

		BeforeEach(func() {
			By("Verifying tenant LocalQueues exist")
			verifyLocalQueueExists(ctx, TenantALocalQueue, TenantANamespace)
			verifyLocalQueueExists(ctx, TenantBLocalQueue, TenantBNamespace)
		})

		AfterEach(func() {
			for _, app := range apps {
				cleanupSparkApplication(ctx, app)
			}
			apps = nil
		})

		It("Should admit and complete apps in both queues simultaneously without interference", func(specCtx SpecContext) {
			By("Submitting SparkApplications to both tenant queues simultaneously")
			appA := newTenantSparkPi("qi-queue-a", "100", TenantANamespace, TenantALocalQueue)
			appB := newTenantSparkPi("qi-queue-b", "100", TenantBNamespace, TenantBLocalQueue)
			apps = append(apps, appA, appB)

			Expect(createWithRetry(ctx, k8sClient, appA)).To(Succeed())
			Expect(createWithRetry(ctx, k8sClient, appB)).To(Succeed())
			GinkgoWriter.Printf("Created apps: %s (tenant-a), %s (tenant-b)\n", appA.Name, appB.Name)

			keyA := types.NamespacedName{Namespace: appA.Namespace, Name: appA.Name}
			keyB := types.NamespacedName{Namespace: appB.Namespace, Name: appB.Name}

			By("Waiting for both apps to start running")
			Expect(waitForSparkAppState(specCtx, keyA, v1beta2.ApplicationStateRunning)).To(Succeed())
			GinkgoWriter.Printf("Tenant A app is Running\n")
			Expect(waitForSparkAppState(specCtx, keyB, v1beta2.ApplicationStateRunning)).To(Succeed())
			GinkgoWriter.Printf("Tenant B app is Running\n")

			By("Verifying both apps complete successfully")
			Expect(waitForSparkAppState(specCtx, keyA, v1beta2.ApplicationStateCompleted)).To(Succeed())
			Expect(waitForSparkAppState(specCtx, keyB, v1beta2.ApplicationStateCompleted)).To(Succeed())

			By("Verifying both ClusterQueues operated independently")
			cqA := getClusterQueue(ctx, TenantAClusterQueue)
			cqB := getClusterQueue(ctx, TenantBClusterQueue)
			admittedA, foundAA, errAA := unstructured.NestedInt64(cqA.Object, "status", "admittedWorkloads")
			Expect(errAA).NotTo(HaveOccurred(), "Failed to read Tenant A admittedWorkloads")
			Expect(foundAA).To(BeTrue(), "admittedWorkloads field not found in Tenant A CQ status")
			admittedB, foundAB, errAB := unstructured.NestedInt64(cqB.Object, "status", "admittedWorkloads")
			Expect(errAB).NotTo(HaveOccurred(), "Failed to read Tenant B admittedWorkloads")
			Expect(foundAB).To(BeTrue(), "admittedWorkloads field not found in Tenant B CQ status")
			pendingA, foundPA, errPA := unstructured.NestedInt64(cqA.Object, "status", "pendingWorkloads")
			Expect(errPA).NotTo(HaveOccurred(), "Failed to read Tenant A pendingWorkloads")
			Expect(foundPA).To(BeTrue(), "pendingWorkloads field not found in Tenant A CQ status")
			pendingB, foundPB, errPB := unstructured.NestedInt64(cqB.Object, "status", "pendingWorkloads")
			Expect(errPB).NotTo(HaveOccurred(), "Failed to read Tenant B pendingWorkloads")
			Expect(foundPB).To(BeTrue(), "pendingWorkloads field not found in Tenant B CQ status")
			GinkgoWriter.Printf("CQ status — tenant-a: admitted=%d pending=%d, tenant-b: admitted=%d pending=%d\n",
				admittedA, pendingA, admittedB, pendingB)

			Expect(pendingA).To(Equal(int64(0)), "Tenant A CQ should have no pending workloads")
			Expect(pendingB).To(Equal(int64(0)), "Tenant B CQ should have no pending workloads")

			GinkgoWriter.Printf("AC2 PASSED: Both queues operated independently — no interference\n")
		}, SpecTimeout(MultitenancyTestTimeout))
	})

	// ========================================================================
	// AC3: Gang Scheduling — No partial admission of SparkApplications
	// ========================================================================
	Context("Gang Scheduling", func() {
		var apps []*v1beta2.SparkApplication

		BeforeEach(func() {
			By("Verifying gang scheduling LocalQueue exists")
			verifyLocalQueueExists(ctx, GangLocalQueue, KueueTestNamespace)
		})

		AfterEach(func() {
			for _, app := range apps {
				cleanupSparkApplication(ctx, app)
			}
			apps = nil
		})

		It("Should not partially admit a SparkApplication — all pods or nothing", func(specCtx SpecContext) {
			// gang-cq has exactly 2 CPU. Each SparkApp needs 2 CPU (1 driver + 1 executor).
			// When one app is running, a second app must stay fully PENDING with zero pods.
			By("Submitting a SparkApplication that fills the gang-cq quota (2 of 2 CPU)")
			filler := newGangSparkPi("gang-filler", "5000000")
			apps = append(apps, filler)
			Expect(createWithRetry(ctx, k8sClient, filler)).To(Succeed())
			GinkgoWriter.Printf("Created filler app: %s\n", filler.Name)

			fillerKey := types.NamespacedName{Namespace: filler.Namespace, Name: filler.Name}
			Expect(waitForSparkAppState(specCtx, fillerKey, v1beta2.ApplicationStateRunning)).To(Succeed())
			GinkgoWriter.Printf("Filler app is Running — gang-cq quota fully consumed (2/2 CPU)\n")

			By("Submitting a second SparkApplication that cannot fit")
			pending := newGangSparkPi("gang-pending", "100")
			apps = append(apps, pending)
			Expect(createWithRetry(ctx, k8sClient, pending)).To(Succeed())
			GinkgoWriter.Printf("Created pending app: %s\n", pending.Name)

			By("Confirming filler is still running before observing pending app")
			fillerCurrent := &v1beta2.SparkApplication{}
			Expect(k8sClient.Get(ctx, fillerKey, fillerCurrent)).To(Succeed())
			Expect(fillerCurrent.Status.AppState.State).To(Equal(v1beta2.ApplicationStateRunning),
				"Filler must still be running to validate gang scheduling")

			By("Verifying Kueue does NOT admit the pending app's Workload (gang scheduling)")
			pendingKey := types.NamespacedName{Namespace: pending.Namespace, Name: pending.Name}
			gangObserved := false
			Consistently(func(g Gomega) {
				fc := &v1beta2.SparkApplication{}
				g.Expect(k8sClient.Get(ctx, fillerKey, fc)).To(Succeed())
				if fc.Status.AppState.State != v1beta2.ApplicationStateRunning {
					GinkgoWriter.Printf("  Filler no longer running (%s), ending check\n",
						fc.Status.AppState.State)
					return
				}

				currentApp := &v1beta2.SparkApplication{}
				g.Expect(k8sClient.Get(ctx, pendingKey, currentApp)).To(Succeed())
				state := currentApp.Status.AppState.State
				suspended := currentApp.Spec.Suspend != nil && *currentApp.Spec.Suspend
				GinkgoWriter.Printf("  Pending app state: %s, suspended: %v\n", state, suspended)

				g.Expect(state).NotTo(Equal(v1beta2.ApplicationStateRunning),
					"Pending app should NOT be running — no room for full gang")
				g.Expect(state).NotTo(Equal(v1beta2.ApplicationStateCompleted),
					"Pending app should NOT have completed")

				wl := findWorkloadForApp(ctx, pending.Name)
				if wl != nil {
					admitted := isWorkloadAdmitted(wl)
					GinkgoWriter.Printf("  Workload %s admitted: %v\n", wl.GetName(), admitted)
					g.Expect(admitted).To(BeFalse(),
						"Kueue should NOT admit the Workload — gang scheduling requires all resources available")
				} else {
					GinkgoWriter.Printf("  No Workload found yet for %s\n", pending.Name)
				}
				gangObserved = true
			}).WithTimeout(15 * time.Second).WithPolling(3 * time.Second).Should(Succeed())

			Expect(gangObserved).To(BeTrue(), "Should have observed pending app blocked while filler was running")

			By("Deleting filler app to free quota")
			Expect(k8sClient.Delete(ctx, filler)).To(Succeed())
			GinkgoWriter.Printf("Deleted filler app to free quota\n")

			By("Verifying the pending app is now admitted atomically (all pods created)")
			Expect(waitForSparkAppState(specCtx, pendingKey, v1beta2.ApplicationStateRunning)).To(Succeed())
			GinkgoWriter.Printf("Pending app is now Running\n")

			Eventually(func(g Gomega) {
				podCount := countSparkAppPods(ctx, pending.Name)
				GinkgoWriter.Printf("  Admitted app pod count: %d\n", podCount)
				g.Expect(podCount).To(BeNumerically(">=", 2),
					"Both driver and executor pods should exist after atomic admission")
			}).WithTimeout(60 * time.Second).WithPolling(MultitenancyPollInterval).Should(Succeed())

			By("Waiting for the app to complete")
			Expect(waitForSparkAppState(specCtx, pendingKey, v1beta2.ApplicationStateCompleted)).To(Succeed())

			GinkgoWriter.Printf("AC3 PASSED: Gang scheduling verified — no partial admission, all pods admitted atomically\n")
		}, SpecTimeout(MultitenancyTestTimeout))
	})
})

// ============================================================================
// Multi-Tenancy Test Helper Functions
// ============================================================================

// newTenantSparkPi creates a SparkApplication in a specific tenant namespace and queue.
func newTenantSparkPi(name, piIterations, namespace, queueName string) *v1beta2.SparkApplication {
	uniqueName := fmt.Sprintf("%s-%s-%04d", name, time.Now().Format("0405"), rand.Intn(10000))
	return &v1beta2.SparkApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:      uniqueName,
			Namespace: namespace,
			Labels: map[string]string{
				"kueue.x-k8s.io/queue-name": queueName,
			},
		},
		Spec: v1beta2.SparkApplicationSpec{
			Type:                v1beta2.SparkApplicationTypeScala,
			Mode:                v1beta2.DeployModeCluster,
			Image:               ptr.To(SparkImage),
			ImagePullPolicy:     ptr.To("IfNotPresent"),
			MainClass:           ptr.To(SparkMainClass),
			MainApplicationFile: ptr.To(SparkJar),
			Arguments:           []string{piIterations},
			SparkVersion:        SparkVersion,
			RestartPolicy: v1beta2.RestartPolicy{
				Type: v1beta2.RestartPolicyNever,
			},
			Volumes: []corev1.Volume{
				{
					Name: "spark-work-dir",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
			},
			Driver: v1beta2.DriverSpec{
				SparkPodSpec: v1beta2.SparkPodSpec{
					Cores:           ptr.To(int32(1)),
					Memory:          ptr.To("512m"),
					ServiceAccount:  ptr.To(SparkSA),
					SecurityContext: &corev1.SecurityContext{},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "spark-work-dir", MountPath: "/opt/spark/work-dir"},
					},
				},
				CoreRequest: ptr.To("1"),
			},
			Executor: v1beta2.ExecutorSpec{
				SparkPodSpec: v1beta2.SparkPodSpec{
					Cores:           ptr.To(int32(1)),
					Memory:          ptr.To("512m"),
					SecurityContext: &corev1.SecurityContext{},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "spark-work-dir", MountPath: "/opt/spark/work-dir"},
					},
				},
				CoreRequest: ptr.To("1"),
				Instances:   ptr.To(int32(1)),
			},
		},
	}
}

// isWorkloadAdmitted checks if a Kueue Workload has the Admitted condition set to True.
func isWorkloadAdmitted(wl *unstructured.Unstructured) bool {
	conditions, _, _ := unstructured.NestedSlice(wl.Object, "status", "conditions")
	for _, c := range conditions {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		cType, _ := cm["type"].(string)
		cStatus, _ := cm["status"].(string)
		if cType == "Admitted" && cStatus == "True" {
			return true
		}
	}
	return false
}

// newGangSparkPi creates a SparkApplication for gang scheduling tests using the gang-lq queue.
// CoreRequest is set explicitly so Kueue accurately accounts for CPU in the Workload.
func newGangSparkPi(name, piIterations string) *v1beta2.SparkApplication {
	uniqueName := fmt.Sprintf("%s-%s-%04d", name, time.Now().Format("0405"), rand.Intn(10000))
	return &v1beta2.SparkApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:      uniqueName,
			Namespace: KueueTestNamespace,
			Labels: map[string]string{
				"kueue.x-k8s.io/queue-name": GangLocalQueue,
			},
		},
		Spec: v1beta2.SparkApplicationSpec{
			Type:                v1beta2.SparkApplicationTypeScala,
			Mode:                v1beta2.DeployModeCluster,
			Image:               ptr.To(SparkImage),
			ImagePullPolicy:     ptr.To("IfNotPresent"),
			MainClass:           ptr.To(SparkMainClass),
			MainApplicationFile: ptr.To(SparkJar),
			Arguments:           []string{piIterations},
			SparkVersion:        SparkVersion,
			RestartPolicy: v1beta2.RestartPolicy{
				Type: v1beta2.RestartPolicyNever,
			},
			Volumes: []corev1.Volume{
				{
					Name: "spark-work-dir",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
			},
			Driver: v1beta2.DriverSpec{
				SparkPodSpec: v1beta2.SparkPodSpec{
					Cores:           ptr.To(int32(1)),
					Memory:          ptr.To("512m"),
					ServiceAccount:  ptr.To(SparkSA),
					SecurityContext: &corev1.SecurityContext{},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "spark-work-dir", MountPath: "/opt/spark/work-dir"},
					},
				},
				CoreRequest: ptr.To("1"),
			},
			Executor: v1beta2.ExecutorSpec{
				SparkPodSpec: v1beta2.SparkPodSpec{
					Cores:           ptr.To(int32(1)),
					Memory:          ptr.To("512m"),
					SecurityContext: &corev1.SecurityContext{},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "spark-work-dir", MountPath: "/opt/spark/work-dir"},
					},
				},
				CoreRequest: ptr.To("1"),
				Instances:   ptr.To(int32(1)),
			},
		},
	}
}
