#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CONFIG_FILE="${SCRIPT_DIR}/sync-config.yaml"
STATE_DIR="${REPO_ROOT}/.sync-state"
STATE_FILE="${STATE_DIR}/current.state"
LOG_FILE="${STATE_DIR}/sync.log"
REPORT_FILE="${STATE_DIR}/pr-report.md"

# MIDSTREAM_REMOTE defaults to "origin" (correct in CI).
# Override for local testing: MIDSTREAM_REMOTE=midstream ./sync-v2.sh
MIDSTREAM_REMOTE="${MIDSTREAM_REMOTE:-origin}"

DRY_RUN=false
RESUME=false

usage() {
    cat <<EOF
Usage: $0 [OPTIONS]

Syncs upstream spark-operator into midstream with config-driven conflict
resolution, validation, and a PR with a detailed sync report.

Options:
  --dry-run   Run merge and validation but skip PR creation
  --resume    Resume from the last saved state
  -h, --help  Show this help
EOF
    exit 0
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run)  DRY_RUN=true; shift ;;
        --resume)   RESUME=true; shift ;;
        -h|--help)  usage ;;
        *)          echo "Unknown option: $1"; usage ;;
    esac
done

mkdir -p "$STATE_DIR"

# ---------------------------------------------------------------------------
# Logging
# ---------------------------------------------------------------------------
log() {
    local msg="[$(date '+%Y-%m-%d %H:%M:%S')] $1"
    echo "$msg"
    echo "$msg" >> "$LOG_FILE"
}

# ---------------------------------------------------------------------------
# State management
# ---------------------------------------------------------------------------
save_state() {
    local state="$1"; shift
    STATE="$state"
    cat > "$STATE_FILE" <<STATEEOF
STATE=$state
TIMESTAMP=$(date -u +%Y-%m-%dT%H:%M:%SZ)
UPSTREAM_SHA=${UPSTREAM_SHA:-}
MIDSTREAM_SHA=${MIDSTREAM_SHA:-}
BRANCH_NAME=${BRANCH_NAME:-}
SYNC_DATE=${SYNC_DATE:-}
MERGE_STATUS=${MERGE_STATUS:-}
STATEEOF
    for kv in "$@"; do echo "$kv" >> "$STATE_FILE"; done
    log "State → $state"
}

load_state() {
    if [[ -f "$STATE_FILE" ]]; then
        # shellcheck disable=SC1090
        source "$STATE_FILE"
    else
        STATE="INIT"
    fi
}

# ---------------------------------------------------------------------------
# Config parsing (requires yq v4+)
# ---------------------------------------------------------------------------
SAFE_IGNORE=()
RISKY_IGNORE=()
CI_IGNORE=()
ALL_IGNORE=()
VALIDATION_CMDS=()
UPSTREAM_REPO=""
UPSTREAM_BRANCH=""
MIDSTREAM_BRANCH=""

parse_config() {
    if ! command -v yq &>/dev/null; then
        log "ERROR: yq v4+ is required. https://github.com/mikefarah/yq"
        exit 1
    fi

    UPSTREAM_REPO=$(yq '.upstream.repo' "$CONFIG_FILE")
    UPSTREAM_BRANCH=$(yq '.upstream.branch' "$CONFIG_FILE")
    MIDSTREAM_BRANCH=$(yq '.midstream.branch' "$CONFIG_FILE")

    mapfile -t SAFE_IGNORE   < <(yq '.safe-ignore[]'  "$CONFIG_FILE" 2>/dev/null || true)
    mapfile -t RISKY_IGNORE  < <(yq '.risky-ignore[]'  "$CONFIG_FILE" 2>/dev/null || true)
    mapfile -t CI_IGNORE     < <(yq '.ci-ignore[]'     "$CONFIG_FILE" 2>/dev/null || true)
    mapfile -t VALIDATION_CMDS < <(yq '.validation[]'  "$CONFIG_FILE" 2>/dev/null || true)

    ALL_IGNORE=("${SAFE_IGNORE[@]}" "${CI_IGNORE[@]}")
}

# ---------------------------------------------------------------------------
# Pattern matching — bash glob against a list of patterns
# ---------------------------------------------------------------------------
matches_patterns() {
    local file="$1"; shift
    for pattern in "$@"; do
        # Strip quotes that yq may leave
        pattern="${pattern%\"}"
        pattern="${pattern#\"}"
        # shellcheck disable=SC2254
        case "$file" in
            $pattern) return 0 ;;
        esac
    done
    return 1
}

# ---------------------------------------------------------------------------
# Set up .git/info/attributes for merge=ours
# ---------------------------------------------------------------------------
setup_merge_drivers() {
    git config merge.ours.driver true
    local attrs=".git/info/attributes"
    mkdir -p "$(dirname "$attrs")"
    : > "$attrs"

    local all_patterns=("${ALL_IGNORE[@]}" "${RISKY_IGNORE[@]}")
    for pattern in "${all_patterns[@]}"; do
        pattern="${pattern%\"}"
        pattern="${pattern#\"}"
        if [[ "$pattern" == */ ]]; then
            echo "${pattern}** merge=ours" >> "$attrs"
        elif [[ "$pattern" == *\*\* ]]; then
            echo "$pattern merge=ours" >> "$attrs"
        elif [[ "$pattern" == *\* ]]; then
            echo "${pattern%\*}** merge=ours" >> "$attrs"
        else
            echo "$pattern merge=ours" >> "$attrs"
        fi
    done

    log "Configured merge=ours for $(wc -l < "$attrs") pattern(s)"
}

# ---------------------------------------------------------------------------
# STEP: Fetch upstream and create sync branch
# ---------------------------------------------------------------------------
step_fetch() {
    log "=== FETCH ==="

    SYNC_DATE=$(date +%Y-%m-%d)
    BRANCH_NAME="upstream-sync-${SYNC_DATE}"

    if git remote get-url upstream &>/dev/null; then
        local current_url
        current_url="$(git remote get-url upstream)"
        if [[ "$current_url" != "$UPSTREAM_REPO" && "$current_url" != "${UPSTREAM_REPO}.git" ]]; then
            log "ERROR: upstream remote points to '$current_url', expected '$UPSTREAM_REPO'"
            exit 1
        fi
        git fetch upstream
    else
        git remote add upstream "$UPSTREAM_REPO"
        git fetch upstream
    fi

    git fetch "$MIDSTREAM_REMOTE"

    UPSTREAM_SHA=$(git rev-parse "upstream/${UPSTREAM_BRANCH}")
    MIDSTREAM_SHA=$(git rev-parse "${MIDSTREAM_REMOTE}/${MIDSTREAM_BRANCH}")

    log "Upstream  : $UPSTREAM_SHA (${UPSTREAM_BRANCH})"
    log "Midstream : $MIDSTREAM_SHA (${MIDSTREAM_BRANCH})"

    if git merge-base --is-ancestor "$UPSTREAM_SHA" "$MIDSTREAM_SHA"; then
        log "Already up to date — nothing to sync."
        save_state "UP_TO_DATE"
        return
    fi

    git checkout -B "$MIDSTREAM_BRANCH" "${MIDSTREAM_REMOTE}/${MIDSTREAM_BRANCH}"
    git checkout -b "$BRANCH_NAME" 2>/dev/null || {
        log "Branch $BRANCH_NAME exists, resetting."
        git checkout "$BRANCH_NAME"
        git reset --hard "${MIDSTREAM_REMOTE}/${MIDSTREAM_BRANCH}"
    }

    save_state "FETCHED"
}

# ---------------------------------------------------------------------------
# STEP: Merge upstream
# ---------------------------------------------------------------------------
step_merge() {
    log "=== MERGE ==="

    setup_merge_drivers

    local merge_output merge_exit=0
    merge_output=$(git merge "upstream/${UPSTREAM_BRANCH}" --no-edit 2>&1) || merge_exit=$?
    echo "$merge_output" >> "$LOG_FILE"

    if [[ $merge_exit -eq 0 ]]; then
        if [[ "$merge_output" == *"Already up to date."* ]]; then
            log "Already up to date."
            save_state "UP_TO_DATE"
            return
        fi
        log "Merge completed cleanly."
        MERGE_STATUS="clean"
        save_state "MERGED"
    else
        log "Merge produced conflicts."
        MERGE_STATUS="conflicts"
        save_state "MERGED"
    fi
}

# ---------------------------------------------------------------------------
# STEP: Classify and resolve conflicts
# ---------------------------------------------------------------------------
step_resolve() {
    log "=== RESOLVE ==="

    local conflicted
    conflicted=$(git diff --name-only --diff-filter=U 2>/dev/null || true)

    if [[ -z "$conflicted" ]]; then
        log "No conflicts."
        save_state "RESOLVED"
        return
    fi

    local -a safe_resolved=() risky_resolved=() ci_resolved=() unresolved=()
    local merge_base
    merge_base=$(git merge-base "${MIDSTREAM_REMOTE}/${MIDSTREAM_BRANCH}" "upstream/${UPSTREAM_BRANCH}")

    : > "${STATE_DIR}/risky-report.md"

    while IFS= read -r file; do
        if matches_patterns "$file" "${SAFE_IGNORE[@]}"; then
            git checkout --ours -- "$file" 2>/dev/null && git add "$file"
            safe_resolved+=("$file")
            log "  safe-resolved: $file"

        elif matches_patterns "$file" "${CI_IGNORE[@]}"; then
            git checkout --ours -- "$file" 2>/dev/null && git add "$file"
            ci_resolved+=("$file")
            log "  ci-resolved: $file"

        elif matches_patterns "$file" "${RISKY_IGNORE[@]}"; then
            # Capture what upstream changed before resolving
            {
                echo "### \`$file\`"
                echo '```diff'
                git diff "${merge_base}..upstream/${UPSTREAM_BRANCH}" -- "$file" 2>/dev/null | head -80 || true
                echo '```'
                echo ""
            } >> "${STATE_DIR}/risky-report.md"

            git checkout --ours -- "$file" 2>/dev/null && git add "$file"
            risky_resolved+=("$file")
            log "  risky-resolved: $file  ⚠ review upstream diff"

        else
            unresolved+=("$file")
            log "  UNRESOLVED: $file"
        fi
    done <<< "$conflicted"

    log "Resolved: safe=${#safe_resolved[@]} ci=${#ci_resolved[@]} risky=${#risky_resolved[@]} unresolved=${#unresolved[@]}"

    # Try to handle go.mod/go.sum conflicts automatically
    if [[ ${#unresolved[@]} -gt 0 ]]; then
        local -a still_unresolved=()
        local go_conflicts=false

        for f in "${unresolved[@]}"; do
            if [[ "$f" == "go.mod" || "$f" == "go.sum" ]]; then
                go_conflicts=true
            else
                still_unresolved+=("$f")
            fi
        done

        if [[ "$go_conflicts" == true ]]; then
            log "Attempting to resolve go.mod/go.sum via go mod tidy..."
            git checkout --theirs -- go.mod 2>/dev/null || true
            git checkout --theirs -- go.sum 2>/dev/null || true
            git add go.mod go.sum

            if go mod tidy 2>> "$LOG_FILE"; then
                git add go.mod go.sum
                log "go.mod/go.sum resolved via go mod tidy."
            else
                still_unresolved+=("go.mod" "go.sum")
                log "go mod tidy failed — go.mod/go.sum remain unresolved."
            fi
        fi

        if [[ ${#still_unresolved[@]} -gt 0 ]]; then
            log "ERROR: ${#still_unresolved[@]} file(s) need manual resolution:"
            printf '  - %s\n' "${still_unresolved[@]}" | tee -a "$LOG_FILE"
            printf '%s\n' "${still_unresolved[@]}" > "${STATE_DIR}/unresolved.txt"
            save_state "FAILED" "REASON=unresolved_conflicts"
            exit 1
        fi
    fi

    git commit --no-edit 2>/dev/null || true

    # Audit risky files: the merge=ours driver may have silently kept
    # midstream's version. Compare each risky file against upstream to
    # detect dropped changes — even when there was no conflict.
    audit_risky_files

    save_state "RESOLVED" \
        "SAFE_RESOLVED=${#safe_resolved[@]}" \
        "CI_RESOLVED=${#ci_resolved[@]}" \
        "RISKY_RESOLVED=${RISKY_AUDIT_COUNT:-0}"
}

# ---------------------------------------------------------------------------
# Audit risky files after merge
# ---------------------------------------------------------------------------
audit_risky_files() {
    log "Auditing risky files for dropped upstream changes..."
    local merge_base
    merge_base=$(git merge-base "${MIDSTREAM_REMOTE}/${MIDSTREAM_BRANCH}" "upstream/${UPSTREAM_BRANCH}")

    : > "${STATE_DIR}/risky-report.md"
    RISKY_AUDIT_COUNT=0

    for file in "${RISKY_IGNORE[@]}"; do
        file="${file%\"}"
        file="${file#\"}"

        # What upstream changed in this file since the branches diverged
        local upstream_diff
        upstream_diff=$(git diff "${merge_base}..upstream/${UPSTREAM_BRANCH}" -- "$file" 2>/dev/null || true)

        if [[ -z "$upstream_diff" ]]; then
            continue
        fi

        # Upstream DID change this file. Check if our merged version matches upstream.
        local merged_content upstream_content
        merged_content=$(git show "HEAD:${file}" 2>/dev/null || true)
        upstream_content=$(git show "upstream/${UPSTREAM_BRANCH}:${file}" 2>/dev/null || true)

        if [[ "$merged_content" == "$upstream_content" ]]; then
            continue
        fi

        # Upstream changed this file and our version differs — flag it.
        RISKY_AUDIT_COUNT=$((RISKY_AUDIT_COUNT + 1))
        log "  ⚠ $file — upstream changes not fully incorporated"

        {
            echo "### \`$file\`"
            echo ""
            echo "<details><summary>Upstream changes since merge base (may be truncated)</summary>"
            echo ""
            echo '```diff'
            echo "$upstream_diff" | head -100
            echo '```'
            echo ""
            echo "</details>"
            echo ""
        } >> "${STATE_DIR}/risky-report.md"
    done

    if [[ $RISKY_AUDIT_COUNT -gt 0 ]]; then
        log "Found $RISKY_AUDIT_COUNT risky file(s) with divergent upstream changes."
    else
        log "No risky files diverged from upstream."
    fi
}

# ---------------------------------------------------------------------------
# STEP: Post-merge validation
# ---------------------------------------------------------------------------
step_validate() {
    log "=== VALIDATE ==="

    # go mod tidy (even if merge was clean — deps may need reconciling)
    log "Running go mod tidy..."
    if go mod tidy 2>> "$LOG_FILE"; then
        if ! git diff --quiet go.mod go.sum 2>/dev/null; then
            git add go.mod go.sum
            git commit -m "chore: go mod tidy after upstream sync"
            log "Committed go.mod/go.sum changes from go mod tidy."
        fi
    else
        log "ERROR: go mod tidy failed."
        save_state "VALIDATION_FAILED" "FAILED_CMD=go mod tidy"
        exit 1
    fi

    # Run remaining validation commands (skip "go mod tidy" since we just ran it)
    for cmd in "${VALIDATION_CMDS[@]}"; do
        [[ "$cmd" == "go mod tidy" ]] && continue
        log "Running: $cmd"
        if ! eval "$cmd" >> "$LOG_FILE" 2>&1; then
            log "ERROR: Validation failed — $cmd"
            save_state "VALIDATION_FAILED" "FAILED_CMD=$cmd"
            exit 1
        fi
        log "  ✓ $cmd"
    done

    save_state "VALIDATED"
}

# ---------------------------------------------------------------------------
# STEP: Build PR report and create PR
# ---------------------------------------------------------------------------
build_report() {
    local merge_base
    merge_base=$(git merge-base "${MIDSTREAM_REMOTE}/${MIDSTREAM_BRANCH}" "upstream/${UPSTREAM_BRANCH}" 2>/dev/null || echo "unknown")

    local risky_section=""
    if [[ -s "${STATE_DIR}/risky-report.md" ]]; then
        risky_section=$(cat <<'HEREDOC_WARN'

## ⚠️ Risky files — review needed

The following files conflicted and were auto-resolved by keeping the midstream
version. Upstream made changes that were **dropped**. Review the diffs below
and manually apply any upstream changes that midstream needs.

HEREDOC_WARN
)
        risky_section+=$(cat "${STATE_DIR}/risky-report.md")
    fi

    cat > "$REPORT_FILE" <<REPORT
## Upstream Sync Report

| | |
|---|---|
| **Upstream** | \`${UPSTREAM_REPO}\` @ \`${UPSTREAM_SHA:-unknown}\` (\`${UPSTREAM_BRANCH}\`) |
| **Midstream** | \`${MIDSTREAM_REMOTE}/${MIDSTREAM_BRANCH}\` @ \`${MIDSTREAM_SHA:-unknown}\` |
| **Merge base** | \`${merge_base}\` |
| **Date** | ${SYNC_DATE:-$(date +%Y-%m-%d)} |

## Conflict resolution

| Category | Count | Action |
|---|---|---|
| Merged cleanly | — | Upstream changes applied |
| Safe-ignore | ${SAFE_RESOLVED:-0} | Kept midstream (no review needed) |
| CI-ignore | ${CI_RESOLVED:-0} | Kept midstream branch triggers |
| Risky-ignore | ${RISKY_RESOLVED:-0} | Kept midstream (**review below**) |
| go.mod/go.sum | — | Resolved via \`go mod tidy\` |

## Validation

All post-merge checks passed:
$(for cmd in "${VALIDATION_CMDS[@]}"; do echo "- ✅ \`$cmd\`"; done)
${risky_section}

## Config

Sync rules defined in \`examples/openshift/upstream-sync/sync-config.yaml\`.
REPORT
}

step_create_pr() {
    log "=== CREATE PR ==="

    build_report

    git push "$MIDSTREAM_REMOTE" "$BRANCH_NAME" --force-with-lease 2>&1 | tee -a "$LOG_FILE"

    if ! gh auth status &>/dev/null; then
        log "ERROR: gh not authenticated. PR not created."
        log "Branch pushed. Create PR manually from: $BRANCH_NAME"
        save_state "PUSHED"
        exit 1
    fi

    local pr_url
    pr_url=$(gh pr create \
        --base "$MIDSTREAM_BRANCH" \
        --head "$BRANCH_NAME" \
        --title "Upstream sync ${SYNC_DATE}" \
        --body-file "$REPORT_FILE" 2>&1) || {
        log "ERROR: gh pr create failed: $pr_url"
        save_state "PUSHED"
        exit 1
    }

    log "PR created: $pr_url"
    save_state "PR_CREATED" "PR_URL=$pr_url"
}

# ---------------------------------------------------------------------------
# Main — state machine driver
# ---------------------------------------------------------------------------
main() {
    parse_config

    if [[ "$RESUME" == true ]]; then
        load_state
        log "Resuming from state: $STATE"
    else
        STATE="INIT"
        : > "$LOG_FILE"
    fi

    case "$STATE" in
        UP_TO_DATE)
            log "Already up to date."
            exit 0
            ;;
        FAILED|VALIDATION_FAILED)
            log "Previous run failed (state=$STATE). Fix the issue and re-run with --resume."
            exit 1
            ;;
        PR_CREATED|PUSHED)
            log "PR already created. Nothing to do."
            exit 0
            ;;
    esac

    # Sequential steps — each advances state and returns.
    # On --resume, we skip completed steps.
    if [[ "$STATE" == "INIT" ]]; then
        step_fetch
        [[ "$STATE" == "UP_TO_DATE" ]] && exit 0
    fi

    if [[ "$STATE" == "FETCHED" ]]; then
        step_merge
        [[ "$STATE" == "UP_TO_DATE" ]] && exit 0
    fi

    if [[ "$STATE" == "MERGED" ]]; then
        step_resolve
    fi

    if [[ "$STATE" == "RESOLVED" ]]; then
        step_validate
    fi

    if [[ "$STATE" == "VALIDATED" ]]; then
        if [[ "$DRY_RUN" == true ]]; then
            build_report
            log "Dry run complete. Report at: $REPORT_FILE"
            cat "$REPORT_FILE"
        else
            step_create_pr
        fi
    fi

    log "=== DONE ==="
}

main
