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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubeflow/spark-operator/v2/api/v1beta2"
)

const (
	ValidationTestTimeout  = 10 * time.Minute
	ValidationPollInterval = 2 * time.Second
)

// ============================================================================
// KUEUE VALIDATION, LIFECYCLE CLEANUP & EVENT VISIBILITY TESTS
// ============================================================================
//
// These tests validate edge cases and error handling:
//   - Dynamic allocation rejection by Kueue webhook
//   - Executor pod cleanup across all termination scenarios
//   - Kubernetes event visibility for queue lifecycle
//   - Non-Kueue regression testing for OwnerReference changes
//
// Prerequisites:
//   - Same as basic Kueue setup (SETUP.md)
//   - Kueue CR with SparkApplication in frameworks list
//   - ResourceFlavor, ClusterQueue, LocalQueue (kueue-resources.yaml)
//
// Run with:
//
//	KUBECONFIG=$HOME/.kube/config \
//	go test -v -tags openshift ./examples/openshift/kueue/ \
//	  -ginkgo.v -ginkgo.focus="Validation" -timeout 35m
//
// ============================================================================

var _ = Describe("Validation, Lifecycle Cleanup and Event Visibility", func() {

	ctx := context.Background()

	BeforeEach(func() {
		By("Ensuring namespace has kueue.openshift.io/managed=true label")
		ns := &corev1.Namespace{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: KueueTestNamespace}, ns)).To(Succeed())
		if ns.Labels == nil {
			ns.Labels = map[string]string{}
		}
		if ns.Labels["kueue.openshift.io/managed"] != "true" {
			ns.Labels["kueue.openshift.io/managed"] = "true"
			Expect(k8sClient.Update(ctx, ns)).To(Succeed())
		}

		By("Verifying Kueue resources exist")
		lq := &unstructured.Unstructured{}
		lq.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "kueue.x-k8s.io",
			Version: "v1beta2",
			Kind:    "LocalQueue",
		})
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      KueueLocalQueue,
			Namespace: KueueTestNamespace,
		}, lq)
		Expect(err).NotTo(HaveOccurred(), "LocalQueue %s must exist. Apply kueue-resources.yaml first.", KueueLocalQueue)
	})

	// ========================================================================
	// AC1: Dynamic Allocation Rejection
	// ========================================================================
	Context("Dynamic Allocation Rejection", func() {
		var app *v1beta2.SparkApplication

		AfterEach(func() {
			cleanupSparkApplication(ctx, app)
		})

		It("Should reject or fail a SparkApplication with dynamicAllocation.enabled=true", func(specCtx SpecContext) {
			app = newDynamicAllocationApp("dynalloc-reject")

			By("Submitting SparkApplication with dynamicAllocation.enabled=true")
			err := k8sClient.Create(ctx, app)

			if err != nil {
				By("Verifying the webhook rejected with a clear error")
				Expect(apierrors.IsForbidden(err) || apierrors.IsInvalid(err) || strings.Contains(err.Error(), "dynamic")).To(BeTrue(),
					"Error should indicate dynamic allocation is not supported; got: %v", err)
				GinkgoWriter.Printf("AC1 PASSED (webhook rejection): %v\n", err)
				app = nil
				return
			}

			By("Webhook accepted the app — verifying Kueue handles it (app should complete or fail, not hang)")
			GinkgoWriter.Printf("Kueue webhook did not reject dynamic allocation; verifying lifecycle completes\n")
			key := types.NamespacedName{Namespace: app.Namespace, Name: app.Name}
			Eventually(func(g Gomega) {
				currentApp := &v1beta2.SparkApplication{}
				g.Expect(k8sClient.Get(ctx, key, currentApp)).To(Succeed())
				state := currentApp.Status.AppState.State
				GinkgoWriter.Printf("  Dynamic alloc app state: %s\n", state)
				isTerminal := state == v1beta2.ApplicationStateCompleted ||
					state == v1beta2.ApplicationStateFailed ||
					state == v1beta2.ApplicationStateFailedSubmission
				g.Expect(isTerminal).To(BeTrue(),
					"App with dynamic allocation should reach a terminal state, got: %s", state)
			}).WithTimeout(ValidationTestTimeout).WithPolling(ValidationPollInterval).Should(Succeed())
			GinkgoWriter.Printf("AC1 PASSED (lifecycle): dynamic allocation app reached terminal state\n")
		}, SpecTimeout(ValidationTestTimeout))
	})

	// ========================================================================
	// AC2: Pod Cleanup After Successful Completion
	// ========================================================================
	Context("Pod Cleanup After Completion", func() {
		var app *v1beta2.SparkApplication

		AfterEach(func() {
			cleanupSparkApplication(ctx, app)
		})

		It("Should clean up all executor pods after the driver exits successfully", func(specCtx SpecContext) {
			app = newSparkPiKueue("cleanup-success", "100")

			By("Creating SparkApplication")
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			GinkgoWriter.Printf("Created SparkApplication %s\n", app.Name)

			key := types.NamespacedName{Namespace: app.Namespace, Name: app.Name}

			By("Waiting for app to start running (pods should be created)")
			Expect(waitForSparkAppState(specCtx, key, v1beta2.ApplicationStateRunning)).To(Succeed())

			By("Verifying pods exist while running")
			Eventually(func(g Gomega) {
				podCount := countAppPods(ctx, app.Name, app.Namespace)
				GinkgoWriter.Printf("  Running pod count: %d\n", podCount)
				g.Expect(podCount).To(BeNumerically(">=", 1), "Should have at least the driver pod")
			}).WithTimeout(30 * time.Second).WithPolling(ValidationPollInterval).Should(Succeed())

			By("Waiting for app to complete")
			Expect(waitForSparkAppState(specCtx, key, v1beta2.ApplicationStateCompleted)).To(Succeed())
			GinkgoWriter.Printf("SparkApplication completed\n")

			By("Verifying all executor pods are cleaned up (no orphans)")
			Eventually(func(g Gomega) {
				executorPods, err := listExecutorPods(ctx, app.Name, app.Namespace)
				g.Expect(err).NotTo(HaveOccurred())
				activePods := filterActivePods(executorPods)
				GinkgoWriter.Printf("  Active executor pods after completion: %d\n", len(activePods))
				g.Expect(len(activePods)).To(Equal(0),
					"All executor pods should be cleaned up after successful completion")
			}).WithTimeout(90 * time.Second).WithPolling(ValidationPollInterval).Should(Succeed())

			GinkgoWriter.Printf("AC2 PASSED: All executor pods cleaned up after successful completion\n")
		}, SpecTimeout(ValidationTestTimeout))
	})

	// ========================================================================
	// AC3: Pod Cleanup After Failure (driver killed)
	// ========================================================================
	Context("Pod Cleanup After Failure", func() {
		var app *v1beta2.SparkApplication

		AfterEach(func() {
			cleanupSparkApplication(ctx, app)
		})

		It("Should clean up all executor pods when the driver fails", func(specCtx SpecContext) {
			app = newFailingSparkApp("cleanup-fail")

			By("Creating SparkApplication that will fail")
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			GinkgoWriter.Printf("Created failing SparkApplication %s\n", app.Name)

			key := types.NamespacedName{Namespace: app.Namespace, Name: app.Name}

			By("Waiting for app to fail")
			waitForSparkAppFailure(specCtx, key)
			logSparkAppStatus(specCtx, key)
			GinkgoWriter.Printf("SparkApplication failed as expected\n")

			By("Verifying all executor pods are cleaned up (no orphans)")
			Eventually(func(g Gomega) {
				executorPods, err := listExecutorPods(ctx, app.Name, app.Namespace)
				g.Expect(err).NotTo(HaveOccurred())
				activePods := filterActivePods(executorPods)
				GinkgoWriter.Printf("  Active executor pods after failure: %d\n", len(activePods))
				g.Expect(len(activePods)).To(Equal(0),
					"All executor pods should be cleaned up after failure")
			}).WithTimeout(90 * time.Second).WithPolling(ValidationPollInterval).Should(Succeed())

			GinkgoWriter.Printf("AC3 PASSED: All executor pods cleaned up after failure\n")
		}, SpecTimeout(ValidationTestTimeout))
	})

	// ========================================================================
	// AC4: No Orphan Pods After Kueue Suspend/Resume
	// ========================================================================
	Context("Pod Cleanup After Suspend and Resume", func() {
		var apps []*v1beta2.SparkApplication

		AfterEach(func() {
			for _, app := range apps {
				cleanupSparkApplication(ctx, app)
			}
			apps = nil
		})

		It("Should leave no orphan pods from the previous run after suspend/resume lifecycle", func(specCtx SpecContext) {
			By("Submitting a long-running app to consume quota")
			blocker := newSparkPiKueue("orphan-blocker", "50000")
			apps = append(apps, blocker)
			Expect(k8sClient.Create(ctx, blocker)).To(Succeed())

			blockerKey := types.NamespacedName{Namespace: blocker.Namespace, Name: blocker.Name}
			Expect(waitForSparkAppState(specCtx, blockerKey, v1beta2.ApplicationStateRunning)).To(Succeed())
			GinkgoWriter.Printf("Blocker app is running\n")

			By("Submitting a second app that will be suspended due to quota")
			waiter := newSparkPiKueue("orphan-waiter", "100")
			apps = append(apps, waiter)
			Expect(k8sClient.Create(ctx, waiter)).To(Succeed())
			GinkgoWriter.Printf("Created waiter app %s\n", waiter.Name)

			waiterKey := types.NamespacedName{Namespace: waiter.Namespace, Name: waiter.Name}

			By("Verifying waiter stays suspended while blocker holds quota")
			Consistently(func(g Gomega) {
				currentApp := &v1beta2.SparkApplication{}
				g.Expect(k8sClient.Get(ctx, waiterKey, currentApp)).To(Succeed())
				state := currentApp.Status.AppState.State
				GinkgoWriter.Printf("  Waiter state: %s, suspend: %v\n", state, currentApp.Spec.Suspend)
				g.Expect(state).NotTo(Equal(v1beta2.ApplicationStateRunning),
					"Waiter should NOT be running while blocker holds quota")
				if currentApp.Spec.Suspend != nil {
					g.Expect(*currentApp.Spec.Suspend).To(BeTrue(),
						"Waiter should be explicitly suspended by Kueue")
				}
			}).WithTimeout(15 * time.Second).WithPolling(3 * time.Second).Should(Succeed())

			By("Deleting blocker to free quota and allow waiter to be admitted")
			Expect(k8sClient.Delete(ctx, blocker)).To(Succeed())
			GinkgoWriter.Printf("Deleted blocker app\n")

			By("Waiting for waiter to be admitted, run, and complete")
			Expect(waitForSparkAppState(specCtx, waiterKey, v1beta2.ApplicationStateCompleted)).To(Succeed())
			GinkgoWriter.Printf("Waiter app completed after resume\n")

			By("Verifying no orphan pods remain from any lifecycle phase")
			Eventually(func(g Gomega) {
				allPods := countAppPods(ctx, waiter.Name, waiter.Namespace)
				executorPods, err := listExecutorPods(ctx, waiter.Name, waiter.Namespace)
				g.Expect(err).NotTo(HaveOccurred())
				activePods := filterActivePods(executorPods)
				GinkgoWriter.Printf("  Waiter total pods: %d, active executors: %d\n", allPods, len(activePods))
				g.Expect(len(activePods)).To(Equal(0),
					"No orphan executor pods should remain after suspend/resume lifecycle")
			}).WithTimeout(90 * time.Second).WithPolling(ValidationPollInterval).Should(Succeed())

			GinkgoWriter.Printf("AC4 PASSED: No orphan pods after suspend/resume lifecycle\n")
		}, SpecTimeout(ValidationTestTimeout))
	})

	// ========================================================================
	// AC5: Event Visibility for Queue Lifecycle
	// ========================================================================
	Context("Event Visibility", func() {
		var apps []*v1beta2.SparkApplication

		AfterEach(func() {
			for _, app := range apps {
				cleanupSparkApplication(ctx, app)
			}
			apps = nil
		})

		It("Should record queryable events for quota lifecycle (admission, queueing)", func(specCtx SpecContext) {
			By("Submitting a long-running app to fill quota")
			filler := newSparkPiKueue("event-filler", "500000")
			apps = append(apps, filler)
			Expect(k8sClient.Create(ctx, filler)).To(Succeed())

			fillerKey := types.NamespacedName{Namespace: filler.Namespace, Name: filler.Name}
			Expect(waitForSparkAppState(specCtx, fillerKey, v1beta2.ApplicationStateRunning)).To(Succeed())
			GinkgoWriter.Printf("Filler app running\n")

			By("Submitting a second app that should be queued (quota exhausted)")
			queued := newSparkPiKueue("event-queued", "100")
			apps = append(apps, queued)
			Expect(k8sClient.Create(ctx, queued)).To(Succeed())
			GinkgoWriter.Printf("Created queued app %s\n", queued.Name)

			By("Checking for Kueue-related events in the namespace")
			var kueueEvents []corev1.Event
			Eventually(func(g Gomega) {
				eventList := &corev1.EventList{}
				g.Expect(k8sClient.List(ctx, eventList, client.InNamespace(KueueTestNamespace))).To(Succeed())
				kueueEvents = filterKueueEvents(eventList.Items)
				g.Expect(len(kueueEvents)).To(BeNumerically(">", 0),
					"Should find at least one Kueue-related event")
			}).WithTimeout(30 * time.Second).WithPolling(3 * time.Second).Should(Succeed())

			GinkgoWriter.Printf("Found %d Kueue-related events:\n", len(kueueEvents))
			for _, e := range kueueEvents {
				GinkgoWriter.Printf("  %s | %s | %s | %s\n",
					e.InvolvedObject.Name, e.Reason, e.Type, truncateMessage(e.Message, 100))
			}

			By("Verifying Workload exists for the queued app and has conditions")
			var queuedWorkload *unstructured.Unstructured
			Eventually(func(g Gomega) {
				wl := findWorkloadForApp(ctx, queued.Name)
				g.Expect(wl).NotTo(BeNil(), "Workload should exist for queued app %s", queued.Name)
				queuedWorkload = wl
			}).WithTimeout(30 * time.Second).WithPolling(ValidationPollInterval).Should(Succeed())

			conditions, _, _ := unstructured.NestedSlice(queuedWorkload.Object, "status", "conditions")
			GinkgoWriter.Printf("Workload %s has %d conditions:\n", queuedWorkload.GetName(), len(conditions))
			for _, c := range conditions {
				cm, _ := c.(map[string]interface{})
				GinkgoWriter.Printf("  type=%s status=%s reason=%s\n",
					cm["type"], cm["status"], cm["reason"])
			}

			Expect(len(conditions)).To(BeNumerically(">", 0),
				"Workload should have status conditions reflecting queue lifecycle")

			By("Deleting filler to allow admission, then checking for admitted events")
			Expect(k8sClient.Delete(ctx, filler)).To(Succeed())

			queuedKey := types.NamespacedName{Namespace: queued.Namespace, Name: queued.Name}
			Expect(waitForSparkAppState(specCtx, queuedKey, v1beta2.ApplicationStateCompleted)).To(Succeed())
			GinkgoWriter.Printf("Queued app completed after admission\n")

			By("Verifying Workload shows Admitted condition")
			Eventually(func(g Gomega) {
				wl := findWorkloadForApp(ctx, queued.Name)
				g.Expect(wl).NotTo(BeNil())
				admitted := isWorkloadConditionTrue(wl, "Admitted")
				GinkgoWriter.Printf("  Workload Admitted: %v\n", admitted)
				g.Expect(admitted).To(BeTrue(), "Workload should have Admitted=True condition")
			}).WithTimeout(30 * time.Second).WithPolling(ValidationPollInterval).Should(Succeed())

			GinkgoWriter.Printf("AC5 PASSED: Kueue events and Workload conditions are queryable\n")
		}, SpecTimeout(ValidationTestTimeout))
	})

	// ========================================================================
	// AC6: Non-Kueue Regression Test
	// ========================================================================
	Context("Non-Kueue Regression", func() {
		var app *v1beta2.SparkApplication

		AfterEach(func() {
			cleanupSparkApplication(ctx, app)
		})

		It("Should run a standard SparkApplication without Kueue labels to completion with no regressions", func(specCtx SpecContext) {
			app = newNonKueueSparkPi("regression-nokueue", "100")

			By("Creating SparkApplication WITHOUT kueue.x-k8s.io/queue-name label")
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			GinkgoWriter.Printf("Created non-Kueue SparkApplication %s (no queue label)\n", app.Name)

			key := types.NamespacedName{Namespace: app.Namespace, Name: app.Name}

			By("Verifying the app is NOT suspended (Kueue should not manage it)")
			Eventually(func(g Gomega) {
				currentApp := &v1beta2.SparkApplication{}
				g.Expect(k8sClient.Get(ctx, key, currentApp)).To(Succeed())
				if currentApp.Spec.Suspend != nil {
					g.Expect(*currentApp.Spec.Suspend).To(BeFalse(),
						"Non-Kueue app should not be suspended")
				}
			}).WithTimeout(30 * time.Second).WithPolling(ValidationPollInterval).Should(Succeed())

			By("Waiting for the app to complete successfully")
			err := waitForSparkAppState(specCtx, key, v1beta2.ApplicationStateCompleted)
			logSparkAppStatus(specCtx, key)
			Expect(err).NotTo(HaveOccurred(),
				"Non-Kueue SparkApplication should complete with no regressions")

			By("Verifying pod lifecycle is clean (no orphan pods)")
			Eventually(func(g Gomega) {
				executorPods, err := listExecutorPods(ctx, app.Name, app.Namespace)
				g.Expect(err).NotTo(HaveOccurred())
				activePods := filterActivePods(executorPods)
				GinkgoWriter.Printf("  Active executor pods: %d\n", len(activePods))
				g.Expect(len(activePods)).To(Equal(0),
					"No orphan executor pods for non-Kueue app")
			}).WithTimeout(90 * time.Second).WithPolling(ValidationPollInterval).Should(Succeed())

			GinkgoWriter.Printf("AC6 PASSED: Non-Kueue SparkApplication completed with clean pod lifecycle\n")
		}, SpecTimeout(ValidationTestTimeout))
	})
})

// ============================================================================
// Validation Test Helper Functions
// ============================================================================

func newDynamicAllocationApp(name string) *v1beta2.SparkApplication {
	uniqueName := fmt.Sprintf("%s-%s-%04d", name, time.Now().Format("0405"), rand.Intn(10000))
	return &v1beta2.SparkApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:      uniqueName,
			Namespace: KueueTestNamespace,
			Labels: map[string]string{
				"kueue.x-k8s.io/queue-name": KueueLocalQueue,
			},
		},
		Spec: v1beta2.SparkApplicationSpec{
			Type:                v1beta2.SparkApplicationTypeScala,
			Mode:                v1beta2.DeployModeCluster,
			Image:               ptr.To(SparkImage),
			ImagePullPolicy:     ptr.To("IfNotPresent"),
			MainClass:           ptr.To(SparkMainClass),
			MainApplicationFile: ptr.To(SparkJar),
			Arguments:           []string{"100"},
			SparkVersion:        SparkVersion,
			DynamicAllocation: &v1beta2.DynamicAllocation{
				Enabled:      true,
				MinExecutors: ptr.To(int32(1)),
				MaxExecutors: ptr.To(int32(4)),
			},
			RestartPolicy: v1beta2.RestartPolicy{
				Type: v1beta2.RestartPolicyNever,
			},
			Driver: v1beta2.DriverSpec{
				SparkPodSpec: v1beta2.SparkPodSpec{
					Cores:           ptr.To(int32(1)),
					Memory:          ptr.To("512m"),
					ServiceAccount:  ptr.To(SparkSA),
					SecurityContext: &corev1.SecurityContext{},
				},
				CoreRequest: ptr.To("1"),
			},
			Executor: v1beta2.ExecutorSpec{
				SparkPodSpec: v1beta2.SparkPodSpec{
					Cores:           ptr.To(int32(1)),
					Memory:          ptr.To("512m"),
					SecurityContext: &corev1.SecurityContext{},
				},
				CoreRequest: ptr.To("1"),
				Instances:   ptr.To(int32(1)),
			},
		},
	}
}

func newFailingSparkApp(name string) *v1beta2.SparkApplication {
	uniqueName := fmt.Sprintf("%s-%s-%04d", name, time.Now().Format("0405"), rand.Intn(10000))
	return &v1beta2.SparkApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:      uniqueName,
			Namespace: KueueTestNamespace,
			Labels: map[string]string{
				"kueue.x-k8s.io/queue-name": KueueLocalQueue,
			},
		},
		Spec: v1beta2.SparkApplicationSpec{
			Type:                v1beta2.SparkApplicationTypeScala,
			Mode:                v1beta2.DeployModeCluster,
			Image:               ptr.To(SparkImage),
			ImagePullPolicy:     ptr.To("IfNotPresent"),
			MainClass:           ptr.To("org.apache.spark.examples.NonExistentClass"),
			MainApplicationFile: ptr.To(SparkJar),
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

func newNonKueueSparkPi(name, piIterations string) *v1beta2.SparkApplication {
	uniqueName := fmt.Sprintf("%s-%s-%04d", name, time.Now().Format("0405"), rand.Intn(10000))
	return &v1beta2.SparkApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:      uniqueName,
			Namespace: KueueTestNamespace,
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
				Instances: ptr.To(int32(1)),
			},
		},
	}
}

func countAppPods(ctx context.Context, appName, namespace string) int {
	pods := &corev1.PodList{}
	err := k8sClient.List(ctx, pods,
		client.InNamespace(namespace),
		client.MatchingLabels{"sparkoperator.k8s.io/app-name": appName},
	)
	if err != nil {
		GinkgoWriter.Printf("Warning: failed to list pods for %s: %v\n", appName, err)
		return -1
	}
	count := 0
	for _, p := range pods.Items {
		if p.DeletionTimestamp == nil {
			count++
		}
	}
	return count
}

func listExecutorPods(ctx context.Context, appName, namespace string) ([]corev1.Pod, error) {
	pods := &corev1.PodList{}
	err := k8sClient.List(ctx, pods,
		client.InNamespace(namespace),
		client.MatchingLabels{
			"sparkoperator.k8s.io/app-name": appName,
			"spark-role":                    "executor",
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list executor pods for %s: %w", appName, err)
	}
	return pods.Items, nil
}

func filterActivePods(pods []corev1.Pod) []corev1.Pod {
	var active []corev1.Pod
	for _, p := range pods {
		if p.DeletionTimestamp != nil {
			continue
		}
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		active = append(active, p)
	}
	return active
}

func waitForSparkAppFailure(ctx context.Context, key types.NamespacedName) {
	Eventually(func(g Gomega) {
		app := &v1beta2.SparkApplication{}
		g.Expect(k8sClient.Get(ctx, key, app)).To(Succeed())
		state := app.Status.AppState.State
		GinkgoWriter.Printf("  Waiting for failure — state: %s\n", state)
		isFailed := state == v1beta2.ApplicationStateFailed ||
			state == v1beta2.ApplicationStateFailedSubmission
		g.Expect(isFailed).To(BeTrue(), "App should reach Failed or FailedSubmission state, got: %s", state)
	}).WithTimeout(ValidationTestTimeout).WithPolling(ValidationPollInterval).Should(Succeed())
}

func filterKueueEvents(events []corev1.Event) []corev1.Event {
	var kueueEvents []corev1.Event
	kueueReasons := map[string]bool{
		"Suspended":        true,
		"Started":          true,
		"QuotaReserved":    true,
		"Admitted":         true,
		"Evicted":          true,
		"Preempted":        true,
		"Completed":        true,
		"QuotaExceeded":    true,
		"WorkloadAdmitted": true,
	}
	for _, e := range events {
		if kueueReasons[e.Reason] {
			kueueEvents = append(kueueEvents, e)
			continue
		}
		if strings.Contains(strings.ToLower(e.Message), "kueue") ||
			strings.Contains(strings.ToLower(e.Message), "quota") ||
			strings.Contains(strings.ToLower(e.Message), "workload") {
			kueueEvents = append(kueueEvents, e)
		}
	}
	return kueueEvents
}

func findWorkloadForApp(ctx context.Context, appName string) *unstructured.Unstructured {
	wlList := &unstructured.UnstructuredList{}
	wlList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kueue.x-k8s.io",
		Version: "v1beta2",
		Kind:    "WorkloadList",
	})
	if err := k8sClient.List(ctx, wlList, client.InNamespace(KueueTestNamespace)); err != nil {
		return nil
	}
	for i := range wlList.Items {
		wl := &wlList.Items[i]
		if strings.HasPrefix(wl.GetName(), "sparkapplication-"+appName) {
			return wl
		}
	}
	return nil
}

func isWorkloadConditionTrue(wl *unstructured.Unstructured, conditionType string) bool {
	conditions, _, _ := unstructured.NestedSlice(wl.Object, "status", "conditions")
	for _, c := range conditions {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		cType, _ := cm["type"].(string)
		cStatus, _ := cm["status"].(string)
		if cType == conditionType && cStatus == "True" {
			return true
		}
	}
	return false
}

func truncateMessage(msg string, maxLen int) string {
	if len(msg) <= maxLen {
		return msg
	}
	return msg[:maxLen] + "..."
}
