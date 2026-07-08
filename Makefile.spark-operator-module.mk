SPARK_OPERATOR_MODULE_IMG ?= spark-operator-module-controller
KO_DOCKER_REPO ?= quay.io/opendatahub
TAG ?= latest
ENGINE ?= $(CONTAINER_TOOL)

.PHONY: docker-build-spark-operator-module docker-push-spark-operator-module deploy-spark-operator-module \
	kustomize-build-spark-operator-module generate-spark-operator-module manifests-spark-operator-module \
	test-spark-operator-module setup-envtest-spark-operator-module precommit-som \
	check-som

docker-build-spark-operator-module:
	${ENGINE} buildx build ${ARCH} --load \
		-t ${KO_DOCKER_REPO}/${SPARK_OPERATOR_MODULE_IMG}:${TAG} \
		-f spark-operator-module-controller.Dockerfile .

docker-push-spark-operator-module: docker-build-spark-operator-module
	${ENGINE} push ${KO_DOCKER_REPO}/${SPARK_OPERATOR_MODULE_IMG}:${TAG}

kustomize-build-spark-operator-module:
	$(KUSTOMIZE) build spark-operator-module/config/default

deploy-spark-operator-module:
	cd spark-operator-module/config/default && $(KUSTOMIZE) edit set image \
		spark-operator-module-controller=${KO_DOCKER_REPO}/${SPARK_OPERATOR_MODULE_IMG}:${TAG}
	$(KUSTOMIZE) build spark-operator-module/config/default | kubectl apply --server-side=true -f -

generate-spark-operator-module: controller-gen
	@$(CONTROLLER_GEN) object paths=./spark-operator-module/pkg/apis/v1alpha1/...

manifests-spark-operator-module: controller-gen
	@$(CONTROLLER_GEN) rbac:roleName=spark-operator-module-manager-role \
		paths=./spark-operator-module/pkg/sparkoperatormodule \
		output:rbac:artifacts:config=spark-operator-module/config/rbac
	@$(CONTROLLER_GEN) crd \
		paths=./spark-operator-module/pkg/apis/v1alpha1/... \
		output:crd:artifacts:config=spark-operator-module/config/crd

test-spark-operator-module: envtest
	cd spark-operator-module && \
	KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
	go test ./... -count=1

setup-envtest-spark-operator-module: envtest
	@$(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path

precommit-som: generate-spark-operator-module manifests-spark-operator-module test-spark-operator-module
	cd spark-operator-module && go mod tidy && go vet ./... && go build ./...

check-som: precommit-som
	@if [ -n "$$(git status -s spark-operator-module/ spark-operator-module-controller.Dockerfile Makefile.spark-operator-module.mk .github/workflows/spark-operator-module-ci.yaml)" ]; then \
		echo "ERROR: Git working tree is not clean after precommit-som."; \
		echo ""; \
		git status -s spark-operator-module/ spark-operator-module-controller.Dockerfile Makefile.spark-operator-module.mk .github/workflows/spark-operator-module-ci.yaml; \
		echo ""; \
		git diff --stat spark-operator-module/ spark-operator-module-controller.Dockerfile Makefile.spark-operator-module.mk .github/workflows/spark-operator-module-ci.yaml; \
		exit 1; \
	fi
	$(MAKE) manifests generate
	@if [ -n "$$(git status -s config/crd/)" ]; then \
		echo "ERROR: Root codegen produced unexpected CRD changes (cross-contamination from module)."; \
		git diff config/crd/; \
		exit 1; \
	fi
