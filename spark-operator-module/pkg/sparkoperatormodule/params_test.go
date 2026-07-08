package sparkoperatormodule

import (
	"os"
	"testing"

	. "github.com/onsi/gomega"
)

func TestParseParams(t *testing.T) {
	g := NewWithT(t)

	dir := t.TempDir()
	path := dir + "/params.env"
	content := "# comment\nSPARK_OPERATOR_CONTROLLER_IMAGE=old\n\nSPARK_OPERATOR_WEBHOOK_IMAGE=old\n"
	g.Expect(os.WriteFile(path, []byte(content), 0o644)).To(Succeed())

	params, err := parseParams(path)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(params).To(HaveKeyWithValue("SPARK_OPERATOR_CONTROLLER_IMAGE", "old"))
	g.Expect(params).To(HaveKeyWithValue("SPARK_OPERATOR_WEBHOOK_IMAGE", "old"))
}

func TestApplyParams_UpdatesFromEnv(t *testing.T) {
	g := NewWithT(t)

	dir := t.TempDir()
	path := dir + "/params.env"
	g.Expect(os.WriteFile(path, []byte("SPARK_OPERATOR_CONTROLLER_IMAGE=old\nSPARK_OPERATOR_WEBHOOK_IMAGE=old\n"), 0o644)).To(Succeed())

	t.Setenv("RELATED_IMAGE_ODH_SPARK_OPERATOR_IMAGE", "quay.io/test/spark:v1")

	g.Expect(applyParams(dir, sparkOperatorImageParamMap)).To(Succeed())

	params, err := parseParams(path)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(params["SPARK_OPERATOR_CONTROLLER_IMAGE"]).To(Equal("quay.io/test/spark:v1"))
	g.Expect(params["SPARK_OPERATOR_WEBHOOK_IMAGE"]).To(Equal("quay.io/test/spark:v1"))
}
