#!/bin/bash
set -euo pipefail

UPSTREAM_REPO="https://github.com/kubeflow/spark-operator"
UPSTREAM_BRANCH="master"
NO_PR=false
INTERACTIVE=false
MERGE_STRATEGY="theirs"

usage() {
    echo "Usage: $0 [--no-pr] [--choose-midstream-conflicts] [--interactive]"
    echo ""
    echo "Syncs upstream spark-operator into the midstream repository."
    echo ""
    echo "Options:"
    echo "  --no-pr                      Skip creating a GitHub PR"
    echo "  --choose-midstream-conflicts Resolve merge conflicts in favor of midstream (origin) instead of upstream"
    echo "  --interactive                Pause on merge conflicts for manual resolution"
    exit 1
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --no-pr)
            NO_PR=true
            shift
            ;;
        --choose-midstream-conflicts)
            MERGE_STRATEGY="ours"
            shift
            ;;
        --interactive)
            INTERACTIVE=true
            shift
            ;;
        -h|--help)
            usage
            ;;
        *)
            echo "Unknown option: $1"
            usage
            ;;
    esac
done

if [[ "$NO_PR" == false ]]; then
    if ! gh auth status &>/dev/null; then
        echo "Error: gh is not authenticated. Run 'gh auth login' first."
        exit 1
    fi
fi

SYNC_DATE=$(date +%Y-%m-%d)
BRANCH_NAME="upstream-sync-${SYNC_DATE}"

git fetch origin
git checkout -B main origin/main
git reset --hard origin/main

if git remote get-url upstream >/dev/null 2>&1; then
    CURRENT_UPSTREAM_URL="$(git remote get-url upstream)"
    if [[ "$CURRENT_UPSTREAM_URL" != "$UPSTREAM_REPO" ]]; then
        echo "Error: existing 'upstream' remote points to '$CURRENT_UPSTREAM_URL', expected '$UPSTREAM_REPO'."
        exit 1
    fi
    git fetch upstream
else
    git remote add -f upstream "$UPSTREAM_REPO"
fi

git checkout -b "$BRANCH_NAME"

if [[ "$INTERACTIVE" == true ]]; then
    if ! git merge "upstream/${UPSTREAM_BRANCH}" --no-edit; then
        echo ""
        echo "Merge conflicts detected. Resolve them, then run:"
        echo "  git add -A && git commit --no-edit"
        echo "  git push origin ${BRANCH_NAME}"
        exit 1
    fi
else
    git merge "upstream/${UPSTREAM_BRANCH}" -X "$MERGE_STRATEGY" --no-edit
fi

git push origin "$BRANCH_NAME"

if [[ "$NO_PR" == false ]]; then
    gh pr create \
        --title "Upstream sync ${SYNC_DATE}" \
        --body "Automated sync from upstream (${UPSTREAM_REPO}) using \`-X ${MERGE_STRATEGY}\` conflict resolution."
fi

echo "Done. Branch '${BRANCH_NAME}' pushed to origin."
