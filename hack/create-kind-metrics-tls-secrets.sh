#!/usr/bin/env bash
# Create self-signed metrics TLS secrets for clusters that have no OpenShift
# service-ca operator. The RHOAI and ODH overlays mount these secrets as
# required volumes, so the files must be valid PEM or the metrics server
# will not start. Do not run this on OpenShift: the service-ca operator
# must create the secrets from the Service annotations.
set -euo pipefail

namespace="${1:?namespace is required}"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

openssl req -x509 -newkey rsa:2048 -sha256 -days 1 -nodes \
  -keyout "${tmpdir}/tls.key" \
  -out "${tmpdir}/tls.crt" \
  -subj "/CN=spark-operator-metrics"

for name in spark-operator-controller-metrics-certs spark-operator-webhook-metrics-certs; do
  kubectl create secret tls "${name}" \
    --namespace="${namespace}" \
    --cert="${tmpdir}/tls.crt" \
    --key="${tmpdir}/tls.key" \
    --dry-run=client -o yaml | kubectl apply -f -
done
