---
name: ship-release
description: Drive a Telepresence release from a prepared branch through CI, docs, the Releases workflow, and PR merges. Assumes make prepare-release has already been run locally and the branch with that commit was pushed and a PR opened. Use only when the user says "ship the release", "complete the release", or "/ship-release".
---

# ship-release

End-to-end driver for releasing Telepresence. Picks up where `prepare-release` left off and carries the change through:

1. Telepresence release PR: CI green, `ok to test`, and `build_and_test` green
2. Docs PR in `../telepresence.io`
3. Tag push, Releases workflow, merge of both PRs

This skill is explicit-only. A release is publicly visible and partially irreversible. Codex must never invoke this as a side effect of inferred intent.

## Preconditions

Run each check and stop with a clear message if any fails:

1. **CWD is the telepresence repo.** `git rev-parse --show-toplevel` ends in `telepresence`.
2. **`make prepare-release` has been run.** The current HEAD must carry both `vX.Y.Z` and `rpc/vX.Y.Z` annotated tags locally:
   ```bash
   git tag --points-at HEAD | sort
   ```
3. **Branch is pushed.** Capture `tp_branch=$(git branch --show-current)`, then run:
   ```bash
   git ls-remote --exit-code origin "refs/heads/$tp_branch"
   ```
4. **PR exists.** `gh pr view "$tp_branch" --json number,state,url,headRefName`
5. **Sibling docs repo present.** `test -d ../telepresence.io && test -d ../telepresence.io/.git`

Capture once and reuse:
- `tp_branch` - prepare-release branch
- `tp_version` - non-`rpc/` tag from `git tag --points-at HEAD`, such as `v2.28.0`
- `docs_version` - `echo "$tp_version" | sed -E 's/^v([0-9]+\.[0-9]+).*/\1/'`, such as `2.28`
- `pr_number` - from `gh pr view`

## Phase 1: Drive the Telepresence release PR

### Wait for non-build_and_test checks

Use:

```bash
gh pr checks "$tp_branch" --json name,state,conclusion
```

Filter out the row whose name matches `build_and_test`; the label triggers it later. For every remaining row:

- `state == "COMPLETED"` and `conclusion == "SUCCESS"` -> green
- `conclusion` in `FAILURE`, `CANCELLED`, `TIMED_OUT`, or `ACTION_REQUIRED` -> stop and report the failing check name plus a short excerpt from `gh run view <run-id> --log-failed`
- Anything else -> keep waiting

Poll every 180 seconds while checks are still running.

### Trigger build_and_test

Once every non-`build_and_test` check is green:

```bash
gh pr edit "$tp_branch" --add-label "ok to test"
gh pr view "$tp_branch" --json labels
```

### Wait for build_and_test

This job can take up to two hours. Poll `gh pr checks "$tp_branch" --json name,state,conclusion` every 1200-1800 seconds, looking specifically at `build_and_test`.

- Success -> continue to Phase 2
- Failure or cancellation -> stop and report, including `gh run view <run-id> --log-failed`
- Still running after about 2.5 hours -> tell the user and stop

## Phase 2: Create the docs PR

Run this in sibling repo `../telepresence.io`.

1. Pull master:
   ```bash
   git checkout master
   git pull
   ```
2. Create a branch with the same name as the Telepresence PR branch:
   ```bash
   git checkout -b "$tp_branch"
   ```
   If that branch already exists locally, stop and ask whether to reuse, reset, or rename.
3. Export variables:
   ```bash
   export DOCS_VERSION="$docs_version"
   export DOCS_BRANCH="$tp_branch"
   ```
4. Generate:
   ```bash
   make generate-version
   ```
5. Verify output:
   ```bash
   ls versioned_docs/version-"$DOCS_VERSION"
   git status
   ```
   Expect `versioned_docs/version-$DOCS_VERSION/` to exist and `git status` to show useful changes.
6. Create the PR. Add only files that actually changed:
   ```bash
   git add versioned_docs/version-"$DOCS_VERSION" versioned_sidebars docusaurus.config.js versions.json
   git commit -s -S -m "Generate docs for telepresence $tp_version"
   git push -u origin "$tp_branch"
   gh pr create --base master --head "$tp_branch" \
     --title "Generate docs for telepresence $tp_version" \
     --body "Generated with \`make generate-version\` DOCS_VERSION=$docs_version DOCS_BRANCH=$tp_branch."
   ```
7. Monitor docs PR checks with `gh pr checks "$tp_branch" --json name,state,conclusion`. Stop and report if anything fails.

Do not include `Co-Authored-By` or generated-tool trailers in commit messages or PR bodies.

## Phase 3: Release

### Push the release tags

Back in the telepresence repo:

```bash
git push origin "$tp_version" "rpc/$tp_version"
```

This triggers `.github/workflows/release.yaml`. The release PR is still unmerged at this point intentionally; merging now would create a new commit and move the branch tip away from the tagged commit.

### Monitor the Releases workflow

```bash
gh run list --workflow=release.yaml --limit 1 --json databaseId,status,conclusion,url
gh run view <id> --json jobs
```

The workflow requires manual approval of the protected `macos-signing` environment. Wait for this up to 24 hours and surface the workflow URL early so the user can chase the approver.

- If the workflow completes successfully -> continue
- If a job other than `build-macos-pkg` fails -> stop and report
- If `build-macos-pkg` is never approved within 24 hours -> tell the user; per `CLAUDE.md`, the release still ships without `.pkg` installers and the user decides whether to proceed

### Merge both PRs

Both PRs must use merge commits, never squash or rebase:

```bash
gh pr merge "$tp_branch" --merge
```

Then in `../telepresence.io`:

```bash
gh pr merge "$tp_branch" --merge
```

Verify each merged with `gh pr view "$tp_branch" --json state`.

## Stop and report

Do not advance to the next step. Surface the failed step, check/run names, run URLs, and a short excerpt from `gh run view <id> --log-failed`. Do not retry automatically.

## Never do

- Run `make prepare-release`; that is a separate skill and decision.
- Push tags before all required PR checks are green.
- Merge PRs as squash or rebase.
- Skip `ok to test`.
- Approve the `macos-signing` environment programmatically.
- Force-push or delete the release branch.
