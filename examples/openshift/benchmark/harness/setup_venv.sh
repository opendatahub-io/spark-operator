#!/usr/bin/env bash
# Create the benchmark virtual environment with a supported Python interpreter.
set -euo pipefail

HARNESS_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VENV_DIR="${HARNESS_ROOT}/.venv"
PYTHON_BIN="${PYTHON:-python3}"
using_existing_venv=false

if [[ -x "${VENV_DIR}/bin/python" ]]; then
  PYTHON_BIN="${VENV_DIR}/bin/python"
  using_existing_venv=true
elif [[ -d "${VENV_DIR}" ]]; then
  echo "ERROR: Existing virtual environment '${VENV_DIR}' is incomplete." >&2
  echo "       Remove it, then re-run with PYTHON=python3.11." >&2
  exit 1
elif ! command -v "${PYTHON_BIN}" > /dev/null 2>&1; then
  echo "ERROR: Python interpreter '${PYTHON_BIN}' was not found." >&2
  echo "       Re-run with a supported interpreter, for example: PYTHON=python3.11 ./run.sh" >&2
  exit 1
fi

python_version="$("${PYTHON_BIN}" --version 2>&1)"
if [[ ! "${python_version}" =~ ^Python\ 3\.(11|12)\. ]]; then
  echo "ERROR: ${python_version} is unsupported; this benchmark requires Python 3.11 or 3.12." >&2
  if [[ "${using_existing_venv}" == "true" ]]; then
    echo "       Remove '${VENV_DIR}', then re-run with PYTHON=python3.11." >&2
  else
    echo "       Re-run with a supported interpreter, for example: PYTHON=python3.11 ./run.sh" >&2
  fi
  exit 1
fi

if [[ ! -d "${VENV_DIR}" ]]; then
  echo "==> Creating venv and installing harness"
  "${PYTHON_BIN}" -m venv "${VENV_DIR}"
  "${VENV_DIR}/bin/pip" install -U pip
  "${VENV_DIR}/bin/pip" install -r "${HARNESS_ROOT}/requirements.txt"
fi
