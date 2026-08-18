.SILENT:

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

# Directory paths
MAKEFILE_DIR := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))
REPO_ROOT := $(abspath $(MAKEFILE_DIR)/../../..)
SCRIPTS_DIR := $(REPO_ROOT)/examples/openshift/scripts
CONTEXT ?= $(REPO_ROOT)

# Include repo root bin/ in PATH so tools installed by the root Makefile
# (e.g. kind via 'make kind') are available to the shell scripts.
LOCALBIN := $(REPO_ROOT)/bin
export PATH := $(LOCALBIN):$(PATH)

# Configuration
KIND_CLUSTER_NAME ?= spark-operator
CLEANUP ?= true
IMAGE_TAG ?= local

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-30s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Cluster Setup

.PHONY: kind-setup
kind-setup: ## Setup a local Kind cluster for testing.
	@echo "Setting up Kind cluster..."
	chmod +x $(SCRIPTS_DIR)/setup-docling-kind-cluster.sh
	$(SCRIPTS_DIR)/setup-docling-kind-cluster.sh

.PHONY: kind-cleanup
kind-cleanup: ## Delete the Kind cluster and cleanup resources.
	@echo "Cleaning up Kind cluster..."
	chmod +x $(SCRIPTS_DIR)/cleanup-kind-cluster.sh
	$(SCRIPTS_DIR)/cleanup-kind-cluster.sh

##@ Tests
## All tests are Go-based and live under test/e2e/rhoai/.
## Use INSTALL_METHOD to control how the operator is deployed.
## Use CLEANUP=false to skip cleanup (default: true).

.PHONY: e2e-rhoai-test
e2e-rhoai-test: ## Run midstream RHOAI e2e tests (kustomize or preinstalled).
	@echo "Running RHOAI e2e tests (install_method=$(INSTALL_METHOD))..."
	cd $(REPO_ROOT) && INSTALL_METHOD=$(INSTALL_METHOD) SPARK_OPERATOR_IMAGE=$(SPARK_OPERATOR_IMAGE) \
		go test ./test/e2e/rhoai/ -v -ginkgo.v -timeout 30m -coverprofile cover-e2e-rhoai.out

.PHONY: e2e-kueue-test
e2e-kueue-test: ## Run Kueue integration tests (requires OpenShift + Kueue).
	@echo "Running Kueue integration tests..."
	cd $(REPO_ROOT) && go test -tags openshift ./test/e2e/rhoai/kueue/ -v -ginkgo.v -timeout 35m

.PHONY: e2e-upstream-kustomize
e2e-upstream-kustomize: ## Run upstream e2e tests with Kustomize install method.
	@echo "Running upstream e2e tests with kustomize deploy..."
	cd $(REPO_ROOT) && DEPLOY_METHOD=kustomize IMAGE_TAG=$(IMAGE_TAG) \
		go test ./test/e2e/ -v -ginkgo.v -timeout 30m

.PHONY: test-all
test-all: ## Run all e2e tests (upstream + midstream).
	@echo "Running all e2e tests..."
	$(MAKE) e2e-upstream-kustomize
	$(MAKE) e2e-rhoai-test

##@ Image Build/Load (CI and local)

.PHONY: docker-build-file
docker-build-file: ## Build docker image with a specific Dockerfile. Usage: make docker-build-file DOCKERFILE=path IMAGE=<full_image>
	@if [ -z "$(DOCKERFILE)" ]; then echo "ERROR: DOCKERFILE is required (e.g., DOCKERFILE=examples/openshift/Dockerfile.odh)"; exit 1; fi
	@if [ -z "$(IMAGE)" ]; then echo "ERROR: IMAGE is required (e.g., IMAGE=quay.io/opendatahub/spark-operator:local)"; exit 1; fi
	@DF="$(DOCKERFILE)"; \
	if [[ "$$DF" != /* ]]; then DF="$(REPO_ROOT)/$$DF"; fi; \
	if [ -n "$(PLATFORM)" ]; then \
		echo "Building for platform $(PLATFORM)"; \
		docker build --platform $(PLATFORM) -f "$$DF" -t ${IMAGE} $(CONTEXT) ; \
	else \
		docker build -f "$$DF" -t ${IMAGE} $(CONTEXT) ; \
	fi

.PHONY: kind-load-image-file
kind-load-image-file: ## Load a specifically built image into Kind. Usage: make kind-load-image-file IMAGE=<full_image>
	@if [ -z "$(IMAGE)" ]; then echo "ERROR: IMAGE is required (e.g., IMAGE=quay.io/opendatahub/spark-operator:local)"; exit 1; fi
	kind load docker-image --name $(KIND_CLUSTER_NAME) $(IMAGE)
