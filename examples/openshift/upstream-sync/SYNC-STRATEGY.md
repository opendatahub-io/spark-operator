# Upstream Sync Strategy

## Goal

Sync and test upstream changes as often as possible so the team has confidence that every sync is release-worthy before merge.

## Guiding Principles

Most development work should be done upstream

- Midstream is reserved for:
  - OpenShift-related patches
  - Emergencies
  - OpenShift-specific testing
  - RHOAI deployment configuration

## Sync Process

### Schedule

- A sync PR is opened every **Wednesday** with the exception of the week of an OpenShift AI release code freeze. The larger goal is to do that daily or decrease this weekly interval overtime.

### Merge Strategy

- The sync is performed via `git merge` from the upstream repository
- Conflicts are resolved by taking the upstream version (`-X theirs`)

### Manual Merge Commands

Here are the manual merge commands assuming `origin` is the midstream repository and that `gh` is authenticated and working.

```bash
git checkout -B main origin/main
git remote add -f upstream https://github.com/kubeflow/spark-operator
git fetch upstream
git checkout -b upstream-sync-<date>
git merge upstream/master -X theirs --no-edit
git push origin upstream-sync-<date>
gh pr create --title "Upstream sync <date>" --body "Automated sync from upstream"
```

### Merge Script

[`sync.sh`](sync.sh) automates the merge commands above and opens a PR.

```bash
# Default: merge upstream, resolve conflicts in favor of upstream, open a PR
./sync.sh

# Resolve conflicts in favor of midstream (origin) instead
./sync.sh --choose-midstream-conflicts

# Pause on conflicts and let you resolve them manually
./sync.sh --interactive

# Skip PR creation (useful for local testing or when gh isn't available)
./sync.sh --no-pr
```

Flags can be combined (e.g. `--interactive --no-pr`). The script checks that `gh` is authenticated before starting unless `--no-pr` is passed.

## Testing

All sync PRs must pass midstream CI. In the future as more testing infrastructure is built out sync PRs should be installed and tested on a RHOAI cluster with upgrade testing done as well.

## Releases

- A release for the repository is cut on the day of ODH Code Freeze.
- Releases are done independently of upstream releases or upstream syncs.
