# Build the module operator binary
FROM registry.access.redhat.com/ubi9/go-toolset:1.24 AS builder
ENV PATH="$PATH:/opt/app-root/src/go/bin"

USER root
WORKDIR /go/src/github.com/opendatahub-io/spark-operator-module
COPY spark-operator-module/go.mod  go.mod
COPY spark-operator-module/go.sum  go.sum
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY spark-operator-module/cmd/spark-operator-module/ cmd/spark-operator-module/
COPY spark-operator-module/pkg/              pkg/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOFLAGS=-mod=readonly go build -a -o manager ./cmd/spark-operator-module

# Collect Spark workload operator manifests from this repo
COPY spark-operator-module/hack/  hack/
COPY config/                      ../config/
RUN bash hack/get_spark_manifests.sh

# Runtime
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
RUN microdnf install -y --disablerepo=* --enablerepo=ubi-9-baseos-rpms shadow-utils && \
    microdnf clean all && \
    useradd spark -m -u 1000 && \
    microdnf remove -y shadow-utils
COPY --from=builder /go/src/github.com/opendatahub-io/spark-operator-module/manager /manager
COPY --from=builder /go/src/github.com/opendatahub-io/spark-operator-module/opt/manifests/ /opt/manifests-template/
USER 1000:1000
ENTRYPOINT ["/manager"]
