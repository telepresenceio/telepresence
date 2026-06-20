---
name: ship-release
description: Drive a Telepresence release from a prepared branch all the way through CI, docs, the Releases workflow, and PR merges. Assumes `make prepare-release` has already been run locally and the branch with that commit was pushed and a PR opened. Use when the user says "ship the release", or "complete the release". User-only.
disable-model-invocation: true
---

# ship-release

End-to-end driver for releasing Telepresence. Picks up where `prepare-release` left off and carries the change through:

1. Telepresence release PR (CI green + `ok to test` + `build_and_test` green)
2. Docs PR in `../telepresence.io`
3. Tag push, Releases workflow, merge of both PRs.

This is **user-only** (`disable-model-invocation: true`). A release is publicly visible and partially irreversible (tags push to GitHub, Homebrew updates for GA). Claude must never invoke this on its own.

## Preconditions to verify before doing anything

Run each check and stop with a clear message if any fails:

1. **CWD is the telepresence repo.** `git rev-parse --show-toplevel` ends in `telepresence`.
2. **`make prepare-release` has been run.** The current HEAD must carry both `vX.Y.Z` and `rpc/vX.Y.Z` annotated tags locally:
   ```
   git tag --points-at HEAD | sort
   ```
   Two entries expected. If the tag list is empty or missing the `rpc/` peer, stop — the user needs to run `make prepare-release` first.
3. **Branch is pushed.** Capture `tp_branch=$(git branch --show-current)`, then:
   ```
   git ls-remote --exit-code origin "refs/heads/$tp_branch"
   ```
   If this fails, stop and tell the user to push the branch first.
4. **PR exists.** `gh pr view "$tp_branch" --json number,state,url,headRefName`. If no PR, stop.
5. **Sibling docs repo present.** `test -d ../telepresence.io && test -d ../telepresence.io/.git`. If not, stop.

Capture once and reuse throughout:

- `tp_branch` — name of the prepare-release branch.
- `tp_version` — pick the non-`rpc/` tag from `git tag --points-at HEAD` (e.g. `v2.28.0`).
- `docs_version` — `echo "$tp_version" | sed -E 's/^v([0-9]+\.[0-9]+).*/\1/'` (e.g. `2.28`).
- `pr_number` — from `gh pr view`.

## Phase 1 — Drive the Telepresence release PR

### 1.1 Verify branch and PR (already done in preconditions)

### 1.2 Wait for all checks except `build_and_test` to be green

Use:

```
gh pr checks "$tp_branch" --json name,state,conclusion
```

Filter out the row whose name matches `build_and_test` (it has not been triggered yet — the label triggers it). For every remaining row:

- `state == "COMPLETED"` and `conclusion == "SUCCESS"` → green
- `conclusion ∈ {"FAILURE","CANCELLED","TIMED_OUT","ACTION_REQUIRED"}` → **stop**. Report the failing check name and a short excerpt from `gh run view <run-id> --log-failed`. Do not advance.
- Anything else (`IN_PROGRESS`, `QUEUED`, `PENDING`) → keep waiting.

**Polling cadence:** these checks (lint, unit tests, license, image-scan) typically finish in 5-15 min. Use `ScheduleWakeup` with `delaySeconds=180` while any check is still running. Do not tight-loop with sleeps.

### 1.3 Trigger `build_and_test`

Once every non-`build_and_test` check is green:

```
gh pr edit "$tp_branch" --add-label "ok to test"
```

Confirm the label is set: `gh pr view "$tp_branch" --json labels`.

### 1.4 Wait for `build_and_test` to be green

This job can take up to two hours. Use `ScheduleWakeup` with `delaySeconds` in the **1200-1800** range (cache miss is fine because the wait is long). Poll with the same `gh pr checks` query, looking specifically at the `build_and_test` row.

- Success → continue to Phase 2.
- Failure / cancellation → **stop and report**. Pull failed-step logs with `gh run view <run-id> --log-failed`.
- Still running after ~2.5 hours → tell the user and stop (workflow may be stuck).

## Phase 2 — Create the docs PR

Done in the sibling repo `../telepresence.io`. Each shell step below is a separate Bash call (no `&&` chains), and `cd` to switch repos.

### 2.1 Pull master

```
cd ../telepresence.io
git checkout master
git pull
```

### 2.2 Create branch with the same name as the telepresence PR branch

```
git checkout -b "$tp_branch"
```

If that branch already exists locally from a previous attempt, stop and ask whether to reuse, reset, or rename.

### 2.3 + 2.4 Export variables

```
export DOCS_VERSION="$docs_version"   # e.g. 2.28 — note: no patch number
export DOCS_BRANCH="$tp_branch"
```

(Per CLAUDE.md, `export` in its own Bash call, then use in subsequent calls. Shell state persists between calls in a session.)

### 2.5 Generate

```
make generate-version
```

### 2.6 Verify output

```
ls versioned_docs/version-"$DOCS_VERSION"
git status
```

Expectations:

- **Minor release** (first time this `2.X` is generated): the directory `versioned_docs/version-$DOCS_VERSION/` appears as untracked.
- **Bugfix release** (directory already existed): files within it are modified.

If `versioned_docs/version-$DOCS_VERSION` is absent or `git status` shows no changes, **stop and report** — `make generate-version` did not do anything useful.

### 2.7 Build the site locally before pushing

Netlify (the `deploy/netlify`, `Pages changed`, `Header rules`, `Redirect rules`
checks) and the `Check`/`Lint` GitHub jobs all run `yarn build` (docusaurus
build). Run it locally first so a broken build is caught here, not after a
push-and-wait CI cycle:

```
# If node_modules is absent: yarn install --frozen-lockfile
yarn build
```

- Exit 0 → the production build (including the new `version-$DOCS_VERSION`)
  compiled. Proceed to the PR.
- Non-zero → **stop and do not push.** Read the error; it names the offending
  file and line.

**Most common failure: MDX parse error in `release-notes.mdx`.** A `.mdx` file
is JSX, so literal `{` / `}` **inside an HTML element** like
`<code>{cmd, stdout}</code>` are parsed as JS expressions and fail with
`Could not parse expression with acorn`. (Braces inside Markdown backtick
spans — `` `{tcp|udp}` `` — are safe.) These release-notes files are generated,
so fix the **source**, not the generated copy:

1. In the telepresence repo, edit the offending `CHANGELOG.yml` entry to remove
   the literal braces (rephrase, e.g. `<code>cmd</code>/<code>stdout</code>`, or
   move the snippet into a backtick span), then `make docs-files`.
2. Commit + push that fix to the release branch (it belongs in the release PR).
3. Back in the docs repo, re-run `make generate-version` to re-pull the fixed
   docs, then `yarn build` again before continuing.

### 2.8 Create the PR

```
git add versioned_docs/version-"$DOCS_VERSION" versioned_sidebars docusaurus.config.js versions.json
# (Add only the files that actually changed — git status will tell you which of the
#  above moved; for a fresh minor you'll likely see all of them, for a bugfix only some.)
git commit -s -S -m "Generate docs for telepresence $tp_version"
git push -u origin "$tp_branch"
gh pr create --base master --head "$tp_branch" \
  --title "Generate docs for telepresence $tp_version" \
  --body "Generated with \`make generate-version\` DOCS_VERSION=$docs_version DOCS_BRANCH=$tp_branch."
```

Capture `docs_pr_number` from the `gh pr create` output URL.

Do not include "Co-Authored-By" or "Generated with" trailers in the commit message or the PR body (per global preferences).

### 2.9 Monitor the docs PR checks

```
gh pr checks "$tp_branch" --json name,state,conclusion
```

Same polling rules as Phase 1.2. If anything fails, **stop and report**.

## Phase 3 — Release

`cd` back to the telepresence repo for step 3.1 and 3.3a.

### 3.1 Push the release tags

```
git push origin "$tp_version" "rpc/$tp_version"
```

This triggers the **Releases** workflow (`.github/workflows/release.yaml`). The release PR is still unmerged at this point — that is intentional. Merging now would create a new commit and move the branch tip away from the tagged commit.

### 3.2 Monitor the Releases workflow

```
gh run list --workflow=release.yaml --limit 1 --json databaseId,status,conclusion,url
gh run view <id> --json jobs
```

The workflow requires manual approval of a protected GitHub environment (`macos-signing`) containing secrets for macOS signing. Wait for this up to **24 hours**. Poll with `ScheduleWakeup` at `delaySeconds=1800` (or longer when overnight). Surface the workflow URL early so the user can chase the approver.

- If the workflow completes successfully → continue to 3.3.
- If a job other than `build-macos-pkg` fails → **stop and report**.
- If `build-macos-pkg` itself is never approved within 24h → tell the user; per CLAUDE.md the release still ships without `.pkg` installers, and the user can decide whether to proceed to 3.3 anyway.

### 3.3 Merge both PRs

Order does not matter. Both must use a **merge commit** (CLAUDE.md: never squash, never rebase).

```
# telepresence PR (in telepresence repo)
gh pr merge "$tp_branch" --merge

# docs PR (in ../telepresence.io)
cd ../telepresence.io
gh pr merge "$tp_branch" --merge
```

Verify each merged: `gh pr view "$tp_branch" --json state` should report `MERGED`.

## Long-wait strategy

- Anything under 5 min → don't sleep; just poll once.
- 5-30 min waits (Phase 1.2 non-build_and_test checks) → `ScheduleWakeup` with `delaySeconds=180`.
- 30 min-2h waits (Phase 1.4 `build_and_test`) → `ScheduleWakeup` with `delaySeconds=1200`.
- Hours-to-overnight (Phase 3.2 macOS signing approval) → `ScheduleWakeup` with `delaySeconds=1800` or longer.

Each wake-up: re-fetch state, decide green/red/still-waiting, schedule the next wake or advance.

## What "stop and report" means

- Do not advance to the next numbered step.
- Surface: the step that failed, the check/run name(s), the run URL(s), and a short excerpt from `gh run view <id> --log-failed`.
- Do not retry automatically. Wait for the user to direct.
- Do not delete branches, force-push, or close PRs. The user decides what to do.

## What this skill must NEVER do

- Run `make prepare-release` itself — that's a separate skill and a separate decision.
- Push tags before all required PR checks are green (Phase 1 must complete first).
- Merge PRs as squash or rebase — both repos require merge commits.
- Skip `ok to test` and try to trigger `build_and_test` some other way.
- Approve the `macos-signing` environment programmatically — that requires a human reviewer.
- Force-push or delete the release branch.
