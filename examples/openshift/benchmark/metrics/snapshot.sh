#!/usr/bin/env bash
# Query Thanos with metrics/prometheus-queries.yaml and write prometheus.json.
#
# Usage (from examples/openshift/benchmark):
#   ./metrics/snapshot.sh results/<run-id>
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${1:-}"
if [[ -z "${OUT_DIR}" ]]; then
  echo "usage: $0 results/<run-id>" >&2
  exit 1
fi
mkdir -p "${OUT_DIR}"

if [[ ! -d "${ROOT}/harness/.venv" ]]; then
  python3 -m venv "${ROOT}/harness/.venv"
  "${ROOT}/harness/.venv/bin/pip" install -q -r "${ROOT}/harness/requirements.txt"
fi

"${ROOT}/harness/.venv/bin/python" "${ROOT}/metrics/snapshot.py" \
  --queries "${ROOT}/metrics/prometheus-queries.yaml" \
  --output "${OUT_DIR}/prometheus.json"
