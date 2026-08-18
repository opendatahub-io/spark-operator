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
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubeflow/spark-operator/v2/api/v1beta2"
)

const (
	TeamALocalQueue   = "team-a-lq"
	TeamBLocalQueue   = "team-b-lq"
	TeamAClusterQueue = "team-a-cq"
	TeamBClusterQueue = "team-b-cq"

	PriorityLocalQueue = "priority-lq"

	HighPriorityClass = "spark-high-priority"
	LowPriorityClass  = "spark-low-priority"

	PriorityTestTimeout  = 12 * time.Minute
	PriorityPollInterval = 3 * time.Second
)

// ============================================================================
// KUEUE PRIORITY, FAIR SHARING & PREEMPTION TESTS
// ============================================================================
//
// These tests validate priority-based scheduling, fair sharing policies, and
// preemption behavior for SparkApplications managed by Kueue on OpenShift.
//
// Prerequisites (in addition to basic Kueue setup from SETUP.md):
//   - Kueue CR with fairSharing.enable: true
//   - WorkloadPriorityClasses: spark-high-priority, spark-low-priority
//   - ClusterQueues: team-a-cq, team-b-cq (in cohort spark-cohort)
//   - LocalQueues: team-a-lq, team-b-lq
//   - Non-admin RBAC: spark-nonadmin ServiceAccount
//
// Apply resources:
//   oc apply -f examples/openshift/kueue/kueue-priority-resources.yaml
//   oc apply -f examples/openshift/kueue/spark-nonadmin-rbac.yaml
//
// Run with:
//   KUBECONFIG=$HOME/.kube/config \
//   go test -v -tags openshift ./examples/openshift/kueue/ \
//     -ginkgo.v -ginkgo.focus="Priority" -timeout 45m
//
// ============================================================================

var _ = Describe("Kueue Priority, FairSharing and Preemption", func() {

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

		By("Verifying priority Kueue resources exist")
		verifyLocalQueueExists(ctx, TeamALocalQueue, KueueTestNamespace)
		verifyLocalQueueExists(ctx, TeamBLocalQueue, KueueTestNamespace)
		verifyLocalQueueExists(ctx, PriorityLocalQueue, KueueTestNamespace)
		verifyWorkloadPriorityClassExists(ctx, HighPriorityClass)
		verifyWorkloadPriorityClassExists(ctx, LowPriorityClass)
	})

	// ========================================================================
	// AC1: FairSharing — Team B admitted before Team A when A over fair share
	// ========================================================================
	Context("FairSharing Policy", func() {
		var apps []*v1beta2.SparkApplication

		AfterEach(func() {
			for _, app := range apps {
				cleanupSparkApplication(ctx, app)
			}
			apps = nil
		})

		It("Should admit Team B jobs before Team A's additional submissions when Team A has consumed most capacity", func(specCtx SpecContext) {
			By("Verifying ClusterQueues exist and checking cohort configuration")
			teamACQ := getClusterQueue(ctx, TeamAClusterQueue)
			teamBCQ := getClusterQueue(ctx, TeamBClusterQueue)

			cohortA, _, _ := unstructured.NestedString(teamACQ.Object, "spec", "cohort")
			cohortB, _, _ := unstructured.NestedString(teamBCQ.Object, "spec", "cohort")
			GinkgoWriter.Printf("team-a-cq spec.cohort: %q, team-b-cq spec.cohort: %q\n", cohortA, cohortB)
			if cohortA == "" {
				GinkgoWriter.Printf("NOTE: spec.cohort is empty — this Kueue version likely uses the Cohort CRD.\n")
				GinkgoWriter.Printf("Verify with: oc get cohorts.kueue.x-k8s.io\n")
			}

			By("Team A submits 2 long-running apps (4 CPU total, borrowing from Team B's nominal quota)")
			teamA1 := newSparkPiPriority("fs-team-a-1", "500000", TeamALocalQueue, "")
			teamA2 := newSparkPiPriority("fs-team-a-2", "500000", TeamALocalQueue, "")
			apps = append(apps, teamA1, teamA2)

			Expect(createWithRetry(ctx, k8sClient, teamA1)).To(Succeed())
			GinkgoWriter.Printf("Created Team A app 1: %s\n", teamA1.Name)

			key1 := keyFor(teamA1)
			Expect(waitForSparkAppState(specCtx, key1, v1beta2.ApplicationStateRunning)).To(Succeed())
			GinkgoWriter.Printf("Team A app 1 is Running\n")

			listWorkloads(ctx, "after-A1-running")
			listClusterQueueUsage(ctx, TeamAClusterQueue)
			listClusterQueueUsage(ctx, TeamBClusterQueue)

			Expect(createWithRetry(ctx, k8sClient, teamA2)).To(Succeed())
			GinkgoWriter.Printf("Created Team A app 2: %s\n", teamA2.Name)

			key2 := keyFor(teamA2)
			Expect(waitForSparkAppState(specCtx, key2, v1beta2.ApplicationStateRunning)).To(Succeed())
			GinkgoWriter.Printf("Team A app 2 is Running (borrowing from Team B's quota)\n")

			By("Team B submits an app — should trigger FairSharing reclaim of borrowed resources")
			teamB1 := newSparkPiPriority("fs-team-b-1", "100", TeamBLocalQueue, "")
			apps = append(apps, teamB1)
			Expect(createWithRetry(ctx, k8sClient, teamB1)).To(Succeed())
			GinkgoWriter.Printf("Created Team B app: %s\n", teamB1.Name)

			By("Verifying Team B's app gets admitted (Kueue reclaims borrowed quota from Team A)")
			keyB := keyFor(teamB1)
			err := waitForSparkAppState(specCtx, keyB, v1beta2.ApplicationStateRunning)
			logSparkAppStatus(specCtx, keyB)
			Expect(err).NotTo(HaveOccurred(), "Team B app should be admitted via FairSharing reclaim")

			By("Waiting for FairSharing preemption to evict at least one Team A app")
			evictedStates := map[v1beta2.ApplicationStateType]bool{
				v1beta2.ApplicationStateSuspended:  true,
				v1beta2.ApplicationStateSuspending: true,
				v1beta2.ApplicationStateFailed:     true,
			}
			Eventually(func(g Gomega) {
				a1State := getSparkAppState(ctx, key1)
				a2State := getSparkAppState(ctx, key2)
				GinkgoWriter.Printf("  Team A states — A1: %s, A2: %s\n", a1State, a2State)
				atLeastOneEvicted := evictedStates[a1State] || evictedStates[a2State]
				g.Expect(atLeastOneEvicted).To(BeTrue(),
					"At least one Team A app should be evicted (A1: %s, A2: %s)", a1State, a2State)
			}).WithTimeout(3 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

			teamA1State := getSparkAppState(ctx, key1)
			teamA2State := getSparkAppState(ctx, key2)
			GinkgoWriter.Printf("Final Team A states — A1: %s, A2: %s\n", teamA1State, teamA2State)

			GinkgoWriter.Printf("AC1 PASSED: Team B admitted, Team A borrowing reclaimed via FairSharing\n")
		}, SpecTimeout(PriorityTestTimeout))
	})

	// ========================================================================
	// AC2: Priority-Based Scheduling as non-admin user
	// ========================================================================
	Context("Priority-Based Scheduling", func() {
		var apps []*v1beta2.SparkApplication

		AfterEach(func() {
			for _, app := range apps {
				cleanupSparkApplication(ctx, app)
			}
			apps = nil
		})

		It("Should admit higher-priority jobs before lower-priority ones when quota is constrained", func(specCtx SpecContext) {
			By("Creating a non-admin client impersonating spark-nonadmin ServiceAccount")
			nonadminClient := createNonAdminClient()

			By("Filling quota with a short-lived blocker app on priority-lq (2 CPU CQ, no cohort)")
			blocker := newSparkPiPriority("prio-blocker", "5000", PriorityLocalQueue, "")
			apps = append(apps, blocker)
			Expect(createWithRetry(ctx, k8sClient, blocker)).To(Succeed())
			GinkgoWriter.Printf("Created blocker: %s\n", blocker.Name)

			blockerKey := keyFor(blocker)
			Expect(waitForSparkAppState(specCtx, blockerKey, v1beta2.ApplicationStateRunning)).To(Succeed())
			GinkgoWriter.Printf("Blocker is Running, quota fully consumed (2 of 2 CPU)\n")

			By("Non-admin submits a low-priority SparkApplication")
			lowApp := newSparkPiPriority("prio-low", "100", PriorityLocalQueue, LowPriorityClass)
			apps = append(apps, lowApp)
			Expect(createWithRetry(ctx, nonadminClient, lowApp)).To(Succeed())
			GinkgoWriter.Printf("Non-admin created low-priority app: %s\n", lowApp.Name)

			By("Non-admin submits a high-priority SparkApplication")
			highApp := newSparkPiPriority("prio-high", "100", PriorityLocalQueue, HighPriorityClass)
			apps = append(apps, highApp)
			Expect(createWithRetry(ctx, nonadminClient, highApp)).To(Succeed())
			GinkgoWriter.Printf("Non-admin created high-priority app: %s\n", highApp.Name)

			By("Verifying both priority apps remain pending/suspended while blocker holds quota")
			Consistently(func(g Gomega) {
				lowState := getSparkAppState(ctx, keyFor(lowApp))
				highState := getSparkAppState(ctx, keyFor(highApp))
				GinkgoWriter.Printf("  blocker running — low: %s, high: %s\n", lowState, highState)
				g.Expect(lowState).NotTo(Equal(v1beta2.ApplicationStateRunning),
					"Low-priority app should NOT be running while quota is exhausted")
				g.Expect(highState).NotTo(Equal(v1beta2.ApplicationStateRunning),
					"High-priority app should NOT be running while quota is exhausted")
			}).WithTimeout(15 * time.Second).WithPolling(3 * time.Second).Should(Succeed())

			By("Waiting for blocker to complete and free quota naturally")
			Expect(waitForSparkAppState(specCtx, blockerKey, v1beta2.ApplicationStateCompleted)).To(Succeed())
			GinkgoWriter.Printf("Blocker completed\n")

			listWorkloads(ctx, "after-blocker-completed")
			listClusterQueueUsage(ctx, "priority-cq")

			By("Waiting for Kueue to release blocker quota and admit next workload")
			Eventually(func(g Gomega) {
				cq := getClusterQueue(ctx, "priority-cq")
				admitted, found, err := unstructured.NestedInt64(cq.Object, "status", "admittedWorkloads")
				g.Expect(err).NotTo(HaveOccurred(), "Failed to read admittedWorkloads")
				g.Expect(found).To(BeTrue(), "admittedWorkloads field not found in ClusterQueue status")
				GinkgoWriter.Printf("  priority-cq admittedWorkloads: %d\n", admitted)
				g.Expect(admitted).To(BeNumerically(">=", 1), "Next workload should be admitted after blocker completes")
			}).WithTimeout(60 * time.Second).WithPolling(10 * time.Second).Should(Succeed())

			By("Verifying high-priority app is admitted first (reaches Running or Completed)")
			highReady := false
			Eventually(func() bool {
				highCurrent := &v1beta2.SparkApplication{}
				if err := k8sClient.Get(ctx, keyFor(highApp), highCurrent); err != nil {
					return false
				}
				state := highCurrent.Status.AppState.State
				highReady = state == v1beta2.ApplicationStateRunning || state == v1beta2.ApplicationStateCompleted
				return highReady
			}).WithTimeout(PriorityTestTimeout).WithPolling(PriorityPollInterval).Should(BeTrue(),
				"High-priority app should reach Running or Completed")
			GinkgoWriter.Printf("High-priority app admitted\n")

			By("Waiting for both apps to complete")
			Expect(waitForSparkAppState(specCtx, keyFor(highApp), v1beta2.ApplicationStateCompleted)).To(Succeed())
			err := waitForSparkAppState(specCtx, keyFor(lowApp), v1beta2.ApplicationStateCompleted)
			logSparkAppStatus(specCtx, keyFor(lowApp))
			Expect(err).NotTo(HaveOccurred(), "Low-priority app should complete after high-priority finishes")

			By("Verifying priority ordering via Workload admission timestamps")
			highWl := findWorkloadForApp(ctx, highApp.Name)
			lowWl := findWorkloadForApp(ctx, lowApp.Name)
			Expect(highWl).NotTo(BeNil(), "High-priority workload should exist")
			Expect(lowWl).NotTo(BeNil(), "Low-priority workload should exist")

			highAdmitTime := getWorkloadAdmissionTime(highWl)
			lowAdmitTime := getWorkloadAdmissionTime(lowWl)
			GinkgoWriter.Printf("High-priority admitted at: %v\n", highAdmitTime)
			GinkgoWriter.Printf("Low-priority admitted at: %v\n", lowAdmitTime)
			Expect(highAdmitTime.IsZero()).To(BeFalse(), "High-priority workload should have admission time")
			Expect(lowAdmitTime.IsZero()).To(BeFalse(), "Low-priority workload should have admission time")
			Expect(highAdmitTime.Before(lowAdmitTime)).To(BeTrue(),
				"High-priority workload should be admitted before low-priority workload")

			GinkgoWriter.Printf("AC2 PASSED: Jobs admitted in correct priority order by non-admin user\n")
		}, SpecTimeout(PriorityTestTimeout))
	})

	// ========================================================================
	// AC3 + AC4: Preemption lifecycle and resume from scratch
	// ========================================================================
	Context("Preemption and Resume Lifecycle", func() {
		var apps []*v1beta2.SparkApplication

		AfterEach(func() {
			for _, app := range apps {
				cleanupSparkApplication(ctx, app)
			}
			apps = nil
		})

		It("Should preempt a running low-priority app when a high-priority app is submitted", func(specCtx SpecContext) {
			By("Submitting a long-running LOW-priority SparkApplication")
			lowApp := newSparkPiPriority("preempt-low", "50000", PriorityLocalQueue, LowPriorityClass)
			apps = append(apps, lowApp)
			Expect(createWithRetry(ctx, k8sClient, lowApp)).To(Succeed())
			GinkgoWriter.Printf("Created low-priority app: %s\n", lowApp.Name)

			lowKey := keyFor(lowApp)
			Expect(waitForSparkAppState(specCtx, lowKey, v1beta2.ApplicationStateRunning)).To(Succeed())
			GinkgoWriter.Printf("Low-priority app is Running\n")

			By("Recording initial submission ID and waiting for pods to be scheduled")
			initialApp := &v1beta2.SparkApplication{}
			Expect(k8sClient.Get(ctx, lowKey, initialApp)).To(Succeed())
			initialSubmissionID := initialApp.Status.SubmissionID
			GinkgoWriter.Printf("Initial submission ID: %s\n", initialSubmissionID)

			Eventually(func(g Gomega) {
				podCount := countSparkAppPods(ctx, lowApp.Name)
				GinkgoWriter.Printf("  Waiting for pods — current count: %d\n", podCount)
				g.Expect(podCount).To(BeNumerically(">=", 1), "Should have at least the driver pod")
			}).WithTimeout(60 * time.Second).WithPolling(PriorityPollInterval).Should(Succeed())

			initialPodCount := countSparkAppPods(ctx, lowApp.Name)
			GinkgoWriter.Printf("Initial pod count: %d\n", initialPodCount)

			By("Submitting a HIGH-priority SparkApplication to trigger preemption")
			highApp := newSparkPiPriority("preempt-high", "100", PriorityLocalQueue, HighPriorityClass)
			apps = append(apps, highApp)
			Expect(createWithRetry(ctx, k8sClient, highApp)).To(Succeed())
			GinkgoWriter.Printf("Created high-priority app: %s\n", highApp.Name)

			By("Verifying low-priority app transitions to Suspended state")
			Eventually(func(g Gomega) {
				current := &v1beta2.SparkApplication{}
				g.Expect(k8sClient.Get(ctx, lowKey, current)).To(Succeed())
				state := current.Status.AppState.State
				GinkgoWriter.Printf("  Low-priority state: %s, suspend: %v\n", state, current.Spec.Suspend)
				g.Expect(state).To(Equal(v1beta2.ApplicationStateSuspended),
					"Low-priority app should transition to Suspended via preemption")
			}).WithTimeout(3 * time.Minute).WithPolling(PriorityPollInterval).Should(Succeed())

			By("Verifying all pods for low-priority app are deleted (non-graceful suspend)")
			Eventually(func(g Gomega) {
				podCount := countSparkAppPods(ctx, lowApp.Name)
				GinkgoWriter.Printf("  Low-priority pod count: %d\n", podCount)
				g.Expect(podCount).To(Equal(0),
					"All driver and executor pods should be deleted during non-graceful preemption")
			}).WithTimeout(90 * time.Second).WithPolling(PriorityPollInterval).Should(Succeed())

			GinkgoWriter.Printf("AC3 PASSED: Running → Suspended, all pods deleted (non-graceful suspend)\n")

			By("Verifying high-priority app runs to completion")
			Expect(waitForSparkAppState(specCtx, keyFor(highApp), v1beta2.ApplicationStateCompleted)).To(Succeed())
			GinkgoWriter.Printf("High-priority app completed\n")

			By("Waiting for low-priority app to be re-admitted after quota is freed")
			Expect(waitForSparkAppState(specCtx, lowKey, v1beta2.ApplicationStateRunning)).To(Succeed())
			GinkgoWriter.Printf("Low-priority app is Running again after re-admission\n")

			By("Verifying the app restarted from scratch (new submission ID)")
			resumedApp := &v1beta2.SparkApplication{}
			Expect(k8sClient.Get(ctx, lowKey, resumedApp)).To(Succeed())
			newSubmissionID := resumedApp.Status.SubmissionID
			GinkgoWriter.Printf("New submission ID: %s (was: %s)\n", newSubmissionID, initialSubmissionID)
			Expect(newSubmissionID).NotTo(Equal(initialSubmissionID),
				"Submission ID must change after restart from scratch")

			By("Verifying new pods were created")
			Eventually(func(g Gomega) {
				podCount := countSparkAppPods(ctx, lowApp.Name)
				GinkgoWriter.Printf("  Resumed pod count: %d\n", podCount)
				g.Expect(podCount).To(BeNumerically(">=", 1),
					"New driver pod should exist after resume")
			}).WithTimeout(60 * time.Second).WithPolling(PriorityPollInterval).Should(Succeed())

			By("Waiting for low-priority app to complete after resume")
			Expect(waitForSparkAppState(specCtx, lowKey, v1beta2.ApplicationStateCompleted)).To(Succeed())

			By("Verifying preemption lifecycle is complete")
			finalApp := &v1beta2.SparkApplication{}
			Expect(k8sClient.Get(ctx, lowKey, finalApp)).To(Succeed())
			logSparkAppStatus(specCtx, lowKey)

			GinkgoWriter.Printf("AC4 PASSED: Suspended → Resuming → Running (new submission ID) → Completed\n")
			GinkgoWriter.Printf("Full preemption lifecycle verified: Running → Suspended (pods=0) → Running (new ID: %s) → Completed\n",
				newSubmissionID)
		}, SpecTimeout(PriorityTestTimeout))
	})
})

// ============================================================================
// Priority Test Helper Functions
// ============================================================================

func newSparkPiPriority(name, piIterations, queueName, priorityClass string) *v1beta2.SparkApplication {
	uniqueName := fmt.Sprintf("%s-%s-%04d", name, time.Now().Format("0405"), rand.Intn(10000))
	labels := map[string]string{
		"kueue.x-k8s.io/queue-name": queueName,
	}
	if priorityClass != "" {
		labels["kueue.x-k8s.io/priority-class"] = priorityClass
	}

	return &v1beta2.SparkApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:      uniqueName,
			Namespace: KueueTestNamespace,
			Labels:    labels,
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

func keyFor(app *v1beta2.SparkApplication) types.NamespacedName {
	return types.NamespacedName{Namespace: app.Namespace, Name: app.Name}
}

func getSparkAppState(ctx context.Context, key types.NamespacedName) v1beta2.ApplicationStateType {
	app := &v1beta2.SparkApplication{}
	if err := k8sClient.Get(ctx, key, app); err != nil {
		return v1beta2.ApplicationStateUnknown
	}
	return app.Status.AppState.State
}

func countSparkAppPods(ctx context.Context, appName string) int {
	pods := &corev1.PodList{}
	err := k8sClient.List(ctx, pods,
		client.InNamespace(KueueTestNamespace),
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

func verifyLocalQueueExists(ctx context.Context, name, namespace string) {
	lq := &unstructured.Unstructured{}
	lq.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kueue.x-k8s.io",
		Version: "v1beta2",
		Kind:    "LocalQueue",
	})
	err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, lq)
	Expect(err).NotTo(HaveOccurred(),
		"LocalQueue %s must exist in namespace %s. Apply kueue-priority-resources.yaml first.", name, namespace)
}

func verifyWorkloadPriorityClassExists(ctx context.Context, name string) {
	wpc := &unstructured.Unstructured{}
	wpc.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kueue.x-k8s.io",
		Version: "v1beta2",
		Kind:    "WorkloadPriorityClass",
	})
	err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, wpc)
	Expect(err).NotTo(HaveOccurred(),
		"WorkloadPriorityClass %s must exist. Apply kueue-priority-resources.yaml first.", name)
}

func createNonAdminClient() client.Client {
	nonadminCfg := rest.CopyConfig(cfg)
	nonadminCfg.Impersonate = rest.ImpersonationConfig{
		UserName: "system:serviceaccount:" + KueueTestNamespace + ":spark-nonadmin",
	}
	nonadminClient, err := client.New(nonadminCfg, client.Options{Scheme: k8sClient.Scheme()})
	Expect(err).NotTo(HaveOccurred(), "Failed to create non-admin client")
	return nonadminClient
}

func listWorkloads(ctx context.Context, label string) {
	wlList := &unstructured.UnstructuredList{}
	wlList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kueue.x-k8s.io",
		Version: "v1beta2",
		Kind:    "WorkloadList",
	})
	err := k8sClient.List(ctx, wlList, client.InNamespace(KueueTestNamespace))
	if err != nil {
		GinkgoWriter.Printf("[%s] Failed to list workloads: %v\n", label, err)
		return
	}
	GinkgoWriter.Printf("[%s] Workloads in namespace (%d total):\n", label, len(wlList.Items))
	for _, wl := range wlList.Items {
		queue, _, _ := unstructured.NestedString(wl.Object, "spec", "queueName")
		admitted := false
		finished := false
		conditions, _, _ := unstructured.NestedSlice(wl.Object, "status", "conditions")
		for _, c := range conditions {
			cm, _ := c.(map[string]interface{})
			cType, _ := cm["type"].(string)
			cStatus, _ := cm["status"].(string)
			if cType == "Admitted" && cStatus == "True" {
				admitted = true
			}
			if cType == "Finished" && cStatus == "True" {
				finished = true
			}
		}
		reservation, _, _ := unstructured.NestedString(wl.Object, "status", "admission", "clusterQueue")

		podSets, _, _ := unstructured.NestedSlice(wl.Object, "spec", "podSets")
		var totalCPU, totalMem string
		for _, ps := range podSets {
			psm, _ := ps.(map[string]interface{})
			psName, _ := psm["name"].(string)
			count, _, _ := unstructured.NestedInt64(psm, "count")
			containers, _, _ := unstructured.NestedSlice(psm, "template", "spec", "containers")
			var cpu, mem string
			if len(containers) > 0 {
				cm, _ := containers[0].(map[string]interface{})
				cpu, _, _ = unstructured.NestedString(cm, "resources", "requests", "cpu")
				mem, _, _ = unstructured.NestedString(cm, "resources", "requests", "memory")
			}
			GinkgoWriter.Printf("    podSet=%s count=%d cpu=%s mem=%s\n", psName, count, cpu, mem)
			if totalCPU == "" {
				totalCPU = cpu
			}
			if totalMem == "" {
				totalMem = mem
			}
		}

		GinkgoWriter.Printf("  %s queue=%s reservedIn=%s admitted=%v finished=%v\n",
			wl.GetName(), queue, reservation, admitted, finished)
	}
}

func listClusterQueueUsage(ctx context.Context, cqName string) {
	cq := getClusterQueue(ctx, cqName)
	flavors, _, _ := unstructured.NestedSlice(cq.Object, "status", "flavorsReservation")
	GinkgoWriter.Printf("ClusterQueue %s usage:\n", cqName)
	for _, f := range flavors {
		fm, _ := f.(map[string]interface{})
		fname, _ := fm["name"].(string)
		resources, _, _ := unstructured.NestedSlice(fm, "resources")
		for _, r := range resources {
			rm, _ := r.(map[string]interface{})
			rname, _ := rm["name"].(string)
			total, _ := rm["total"].(string)
			borrowed, _ := rm["borrowed"].(string)
			GinkgoWriter.Printf("  flavor=%s resource=%s total=%s borrowed=%s\n", fname, rname, total, borrowed)
		}
	}
	pending, _, _ := unstructured.NestedInt64(cq.Object, "status", "pendingWorkloads")
	admitted, _, _ := unstructured.NestedInt64(cq.Object, "status", "admittedWorkloads")
	GinkgoWriter.Printf("  admittedWorkloads=%d pendingWorkloads=%d\n", admitted, pending)
}

func createWithRetry(ctx context.Context, c client.Client, obj client.Object) error {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			GinkgoWriter.Printf("  Retrying create (attempt %d/5) after transient error...\n", attempt+1)
			time.Sleep(5 * time.Second)
		}
		lastErr = c.Create(ctx, obj)
		if lastErr == nil {
			return nil
		}
		GinkgoWriter.Printf("  Create failed: %v\n", lastErr)
		if apierrors.IsForbidden(lastErr) || apierrors.IsInvalid(lastErr) ||
			apierrors.IsAlreadyExists(lastErr) || apierrors.IsNotFound(lastErr) {
			return lastErr
		}
	}
	return lastErr
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
		name := wl.GetName()
		if len(name) > 0 && containsAppName(name, appName) {
			return wl
		}
	}
	return nil
}

func containsAppName(workloadName, appName string) bool {
	prefix := "sparkapplication-" + appName
	return strings.HasPrefix(workloadName, prefix)
}

func getWorkloadAdmissionTime(wl *unstructured.Unstructured) time.Time {
	conditions, _, _ := unstructured.NestedSlice(wl.Object, "status", "conditions")
	for _, c := range conditions {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		cType, _ := cm["type"].(string)
		cStatus, _ := cm["status"].(string)
		if cType == "Admitted" && cStatus == "True" {
			ts, _ := cm["lastTransitionTime"].(string)
			t, err := time.Parse(time.RFC3339, ts)
			if err == nil {
				return t
			}
		}
	}
	return time.Time{}
}
