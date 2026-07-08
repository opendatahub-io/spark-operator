package sparkoperatormodule

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCheckDeploymentsReady_Missing(t *testing.T) {
	g := NewWithT(t)

	cli := fake.NewClientBuilder().Build()
	err := checkDeploymentsReady(context.Background(), cli, "opendatahub", []string{"spark-operator-controller"})
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("spark-operator-controller"))
}

func TestCheckDeploymentsReady_Available(t *testing.T) {
	g := NewWithT(t)

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spark-operator-controller",
			Namespace: "opendatahub",
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "spark-operator-controller"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "spark-operator-controller"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "test"}}},
			},
		},
	}
	dep.Status.AvailableReplicas = 1
	dep.Status.UpdatedReplicas = 1

	cli := fake.NewClientBuilder().WithObjects(dep).WithStatusSubresource(dep).Build()
	err := checkDeploymentsReady(context.Background(), cli, "opendatahub", []string{"spark-operator-controller"})
	g.Expect(err).NotTo(HaveOccurred())
}
