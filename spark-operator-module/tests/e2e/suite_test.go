package e2e_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/opendatahub-io/spark-operator-module/pkg/apis/v1alpha1"
)

const (
	ModuleNamespace = "opendatahub"
	DeploymentName  = "spark-operator-module-controller-manager"
	CRDName         = "sparkoperators.components.platform.opendatahub.io"
	CRName          = "default-sparkoperator"

	PollInterval = 2 * time.Second
	WaitTimeout  = 3 * time.Minute
)

var (
	cfg       *rest.Config
	testEnv   *envtest.Environment
	k8sClient client.Client
	clientset *kubernetes.Clientset
	repoRoot  string

	origKustomization []byte
)

func TestModuleOperator(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Module Operator E2E Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	repoRoot = filepath.Join("..", "..", "..")

	By("Bootstrapping test environment")
	testEnv = &envtest.Environment{
		UseExistingCluster: ptr.To(true),
		BinaryAssetsDirectory: filepath.Join(repoRoot, "bin", "k8s",
			fmt.Sprintf("1.33.0-%s-%s", runtime.GOOS, runtime.GOARCH)),
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	Expect(v1alpha1.AddToScheme(scheme.Scheme)).NotTo(HaveOccurred())
	Expect(apiextensionsv1.AddToScheme(scheme.Scheme)).NotTo(HaveOccurred())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())

	clientset, err = kubernetes.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred())

	installModuleOperator()
})

var _ = AfterSuite(func() {
	cleanup := os.Getenv("CLEANUP")
	if strings.EqualFold(cleanup, "false") {
		logf.Log.Info("CLEANUP=false, skipping uninstall")
	} else {
		uninstallModuleOperator()
	}

	if origKustomization != nil {
		kustomizationPath := filepath.Join(repoRoot, "spark-operator-module", "config", "default", "kustomization.yaml")
		Expect(os.WriteFile(kustomizationPath, origKustomization, 0644)).NotTo(HaveOccurred())
	}

	By("Tearing down the test environment")
	Expect(testEnv.Stop()).NotTo(HaveOccurred())
})

func installModuleOperator() {
	kustomizeDir := filepath.Join(repoRoot, "spark-operator-module", "config", "default")
	kustomizationPath := filepath.Join(kustomizeDir, "kustomization.yaml")

	moduleImage := os.Getenv("MODULE_IMAGE")
	if moduleImage != "" {
		By(fmt.Sprintf("Overriding module operator image to %s", moduleImage))
		data, err := os.ReadFile(kustomizationPath)
		Expect(err).NotTo(HaveOccurred())
		origKustomization = data

		imageName, imageTag := parseImageRef(moduleImage)

		content := string(data)
		nameRe := regexp.MustCompile(`(?m)(newName:\s*).*`)
		content = nameRe.ReplaceAllString(content, "${1}"+imageName)
		tagRe := regexp.MustCompile(`(?m)(newTag:\s*).*`)
		content = tagRe.ReplaceAllString(content, "${1}"+imageTag)
		Expect(os.WriteFile(kustomizationPath, []byte(content), 0644)).NotTo(HaveOccurred(),
			"Failed to update kustomization.yaml image")
	}

	By("Creating module namespace")
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ModuleNamespace}}
	if err := k8sClient.Create(context.TODO(), ns); err != nil {
		logf.Log.Info("Namespace already exists or error creating", "error", err)
	}

	By("Installing the module operator via Kustomize")
	applyCmd := exec.Command("kubectl", "apply", "-k", kustomizeDir, "--server-side=true")
	applyCmd.Stdout = GinkgoWriter
	applyCmd.Stderr = GinkgoWriter
	Expect(applyCmd.Run()).NotTo(HaveOccurred(), "Failed to apply module operator Kustomize manifests")

	By("Waiting for module operator deployment to be available")
	waitCmd := exec.Command("kubectl", "rollout", "status",
		fmt.Sprintf("deployment/%s", DeploymentName),
		"-n", ModuleNamespace,
		"--timeout=120s")
	waitCmd.Stdout = GinkgoWriter
	waitCmd.Stderr = GinkgoWriter
	Expect(waitCmd.Run()).NotTo(HaveOccurred(), "Module operator deployment did not become ready")
}

func uninstallModuleOperator() {
	kustomizeDir := filepath.Join(repoRoot, "spark-operator-module", "config", "default")

	By("Deleting SparkOperator CR if it exists")
	deleteCmd := exec.Command("kubectl", "delete", "sparkoperator", CRName, "--ignore-not-found", "--timeout=30s")
	deleteCmd.Stdout = GinkgoWriter
	deleteCmd.Stderr = GinkgoWriter
	_ = deleteCmd.Run()

	By("Uninstalling the module operator via Kustomize")
	uninstallCmd := exec.Command("kubectl", "delete", "-k", kustomizeDir, "--ignore-not-found", "--timeout=120s")
	uninstallCmd.Stdout = GinkgoWriter
	uninstallCmd.Stderr = GinkgoWriter
	_ = uninstallCmd.Run()
}

func parseImageRef(ref string) (name, tag string) {
	if idx := strings.Index(ref, "@"); idx != -1 {
		return ref[:idx], ref[idx+1:]
	}
	if idx := strings.LastIndex(ref, ":"); idx != -1 {
		return ref[:idx], ref[idx+1:]
	}
	return ref, "latest"
}
