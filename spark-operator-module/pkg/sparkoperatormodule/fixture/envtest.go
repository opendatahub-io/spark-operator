package fixture

import (
	"context"
	"os"
	"path/filepath"
	runtimepkg "runtime"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"

	platformv1alpha1 "github.com/opendatahub-io/spark-operator-module/pkg/apis/v1alpha1"
	"github.com/opendatahub-io/spark-operator-module/pkg/sparkoperatormodule"
)

type TestEnv struct {
	Client     client.Client
	Deployer   *MockDeployer
	Reconciler *sparkoperatormodule.SparkOperatorModuleReconciler
}

func SetupTestEnv(ctx context.Context) *TestEnv {
	logf.SetLogger(zap.New(zap.WriteTo(ginkgo.GinkgoWriter), zap.UseDevMode(true)))
	gomega.SetDefaultEventuallyTimeout(30 * time.Second)
	gomega.SetDefaultEventuallyPollingInterval(250 * time.Millisecond)

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(platformv1alpha1.AddToScheme(scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(ProjectRoot(), "config", "crd")},
		ErrorIfCRDPathMissing: true,
		Scheme:                scheme,
	}

	cfg, err := env.Start()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	cli, err := client.New(cfg, client.Options{Scheme: scheme})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:         scheme,
		Metrics:        metricsserver.Options{BindAddress: "0"},
		LeaderElection: false,
	})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	workDir := ginkgo.GinkgoT().TempDir()
	WriteMinimalManifests(workDir)

	deployer := &MockDeployer{}
	reconciler := &sparkoperatormodule.SparkOperatorModuleReconciler{
		Client:                mgr.GetClient(),
		Scheme:                mgr.GetScheme(),
		ManifestsTemplatePath: workDir,
		Deployer:              deployer,
	}
	reconciler.SetWorkDir(workDir)
	reconciler.SetPlatform(cluster.OpenDataHub)
	gomega.Expect(reconciler.SetupWithManager(mgr)).To(gomega.Succeed())

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "opendatahub"}}
	gomega.Expect(cli.Create(ctx, ns)).To(gomega.Succeed())

	mgrCtx, mgrCancel := context.WithCancel(ctx)
	go func() {
		defer ginkgo.GinkgoRecover()
		gomega.Expect(mgr.Start(mgrCtx)).To(gomega.Succeed())
	}()

	ginkgo.DeferCleanup(func() {
		mgrCancel()
		gomega.Expect(env.Stop()).To(gomega.Succeed())
	})

	return &TestEnv{
		Client:     cli,
		Deployer:   deployer,
		Reconciler: reconciler,
	}
}

func ProjectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			_, filename, _, _ := runtimepkg.Caller(0)
			return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
		}
		dir = parent
	}
}

func WriteMinimalManifests(workDir string) {
	manifest := `apiVersion: v1
kind: ConfigMap
metadata:
  name: spark-operator-test-config
  namespace: opendatahub
data:
  test: "true"
`
	componentMetadata := `releases:
  - name: Spark Operator
    version: v2.4.0
    repoUrl: https://github.com/opendatahub-io/spark-operator
`
	overlayDir := filepath.Join(workDir, sparkoperatormodule.SparkOperatorComponentName, sparkoperatormodule.SparkOperatorManifestSourcePathODH)
	writeKustomizeDir(overlayDir, manifest)
	gomega.Expect(os.WriteFile(
		filepath.Join(workDir, sparkoperatormodule.SparkOperatorComponentName, "config", "component_metadata.yaml"),
		[]byte(componentMetadata), 0o644,
	)).To(gomega.Succeed())
	gomega.Expect(os.WriteFile(
		filepath.Join(overlayDir, "params.env"),
		[]byte("SPARK_OPERATOR_CONTROLLER_IMAGE=placeholder\nSPARK_OPERATOR_WEBHOOK_IMAGE=placeholder\n"),
		0o644,
	)).To(gomega.Succeed())
}

func writeKustomizeDir(dir, manifest string) {
	gomega.Expect(os.MkdirAll(dir, 0o755)).To(gomega.Succeed())

	kustomization := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- resource.yaml
`
	gomega.Expect(os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(kustomization), 0o644)).To(gomega.Succeed())
	gomega.Expect(os.WriteFile(filepath.Join(dir, "resource.yaml"), []byte(manifest), 0o644)).To(gomega.Succeed())
}
