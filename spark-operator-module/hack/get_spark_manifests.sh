#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MODULE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
REPO_ROOT="$(cd "${MODULE_DIR}/.." && pwd)"
DST_MANIFESTS_DIR="${1:-${MODULE_DIR}/opt/manifests}"

echo "Collecting Spark workload operator manifests from ${REPO_ROOT}/config"

rm -rf "${DST_MANIFESTS_DIR}"
mkdir -p "${DST_MANIFESTS_DIR}/spark-operator"

cp -R "${REPO_ROOT}/config" "${DST_MANIFESTS_DIR}/spark-operator/"

echo "Manifests collected at ${DST_MANIFESTS_DIR}/spark-operator/config"
