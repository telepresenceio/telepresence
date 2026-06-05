---
name: prepare-release
description: Create the local release commit and tags by setting TELEPRESENCE_VERSION and running make prepare-release. Stops at the local commit+tags; pushing is the ship-release skill's job. Use only when the user explicitly asks to prepare a release, RC, or test build.
---

# prepare-release

Wraps the `make prepare-release` step so the local-tag-creation portion of a release is one explicit user action, not a chain of remembered commands. Stops at local tags; the `ship-release` skill takes over from there.

This skill is explicit-only by design. The tags this skill creates will eventually drive a public release, so creating them must be an explicit user decision. Codex must never invoke this as a side effect of inferred intent.

## Confirm before doing anything

Ask the user explicitly:

1. **Version string** (`TELEPRESENCE_VERSION`) - must be one of:
   - `vX.Y.Z-test.N` - pre-release, no Homebrew, no "latest"
   - `vX.Y.Z-rc.N` - pre-release, no Homebrew, no "latest"
   - `vX.Y.Z` - GA, marked latest, triggers Homebrew update
2. **Branch** - should be a release branch, typically `release/v2`. Refuse to proceed from `main`-style branches.
3. **Working tree** - must be clean. Run `git status`; if there are uncommitted or untracked files relevant to the build, stop.
4. **CHANGELOG.yml status** - the top entry should have a version matching `TELEPRESENCE_VERSION` without the leading `v`. If it is still `date: (TBD)`, that is expected: `make prepare-release` sets the date for GA versions.

Show all four checks to the user before running anything. Wait for explicit "go".

## Steps

```bash
export TELEPRESENCE_VERSION=vX.Y.Z[-suffix.N]
make prepare-release
```

This creates:
- An annotated tag `vX.Y.Z[-suffix.N]`
- An annotated tag `rpc/vX.Y.Z[-suffix.N]`
- A commit that bumps go.mod references inside the repo

Verify with:

```bash
git log -1 --stat
git tag --points-at HEAD
```

## Next: hand off to `ship-release`

This skill stops here, with the local commit and the two annotated tags. **Do not push anything.** To carry the release through CI, the docs PR, the Releases workflow, and the PR merges, invoke `$ship-release` after the user manually pushes the branch and opens a PR on it.

## Refuse to

- Push anything: branch, commit, or tags. That is `ship-release`'s job.
- Skip `make prepare-release` and just tag manually. The make target updates go.mod references; manual tagging skips that and ships a broken module.
- Re-run `make prepare-release` on top of a previous attempt without first cleaning up the leftover tags. If `git tag --points-at HEAD` already lists the target tag, stop and report; the user has to decide whether to delete it.
