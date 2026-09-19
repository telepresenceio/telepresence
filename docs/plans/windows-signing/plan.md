# Signed Windows release binaries via SignPath Foundation

Resolves #4284. Status: proposed.

## Goal

Every Windows artifact in a GitHub release carries a timestamped
Authenticode signature, so an enterprise can allow `telepresence.exe` to run
elevated by publisher instead of per-run admin approval. The signer is the
SignPath Foundation certificate, obtained through their free program for
open-source projects and applied from GitHub Actions.

## Artifacts to sign

| Artifact | Built by | Signed how |
|----------|----------|------------|
| `telepresence.exe` (amd64, arm64) | `make release-binary` on the Windows runner | `<pe-file>` inside the standalone zip |
| `TelepresenceDaemon.exe` | `build-aux/wix-installer/Makefile` from `tpwrapper.go` | `<pe-file>` inside `MainPackage.msi` |
| `MainPackage.msi` | WiX 6 | `<msi-file>` with deep signing of the two exes |
| Burn engine of `TelepresenceInstall.exe` | `wix burn detach` | `<pe-file>` |
| `TelepresenceInstall.exe` (bundle) | WiX 6, after reattach | `<pe-file>` |

`wintun.dll`, `winfsp.msi` and `sshfs-win.msi` are upstream binaries already
signed by their authors. SignPath's terms allow shipping them unsigned by us
inside our packages, so they are left alone.

## Part 1: prerequisites outside the repository

These gate everything else and are the calendar-time cost.

1. **Two-factor authentication** on GitHub for every member who will hold a
   SignPath role. SignPath requires it for both the repository and their
   portal. The `telepresenceio` org can enforce it org-wide; check current
   state with `gh api orgs/telepresenceio` (`two_factor_requirement_enabled`).
2. **Roles.** SignPath wants named Authors (may change source unreviewed),
   Reviewers (approve non-committer changes) and Approvers (authorize each
   signing request). Map them to existing GitHub teams:
   `telepresence-maintainers` as Authors and Reviewers, a new small
   `release-approvers` team, or the `administrators` team, as Approvers.
   The code signing policy has to link these teams.
3. **Privacy statement.** SignPath's default sentence, "This program will
   not transfer any information to other networked systems unless
   specifically requested by the user", is not true for Telepresence
   because of usage reporting. The alternative they accept is a link to a
   privacy policy. `docs/reference/config.md` already documents what the
   usage report contains and every opt-out, so the policy links there; a
   short `PRIVACY.md` at the repo root that points to that section is the
   cleanest target. Decision needed: whether to keep the existing wording
   and only add the pointer, or to write a dedicated privacy page.
4. **Application** at signpath.org/apply. The form asks for the repository
   URL, the license (Apache-2.0), the release download URL
   (`https://github.com/telepresenceio/telepresence/releases`), a project
   description naming the artifact types (CLI exe, MSI, Burn bundle, zip),
   and a maintainer contact. Reviews take from a few days to a few weeks;
   expect follow-up questions about the build and the roles.
5. **After approval**, in the SignPath portal: create the project, link the
   predefined "GitHub.com" trusted build system to the organization and the
   project, add a `release-signing` policy with manual approval and a
   `test-signing` policy without, create a CI user with submitter rights,
   and store its API token as the repository secret `SIGNPATH_API_TOKEN`
   scoped to a new protected environment `windows-signing`. Record the
   organization id, project slug and policy slugs as repository variables
   so they are not hard-coded in the workflow.

## Part 2: repository changes that can land before approval

Nothing here changes a shipped artifact, so it can merge on its own.

1. **Code signing policy section.** Add "Code signing policy" as a section
   header (SignPath requires that exact phrase) to `README.md` and to the
   Windows part of `docs/install/client.md`, containing the mandated
   sentence "Free code signing provided by SignPath.io, certificate by
   SignPath Foundation", the role-to-team mapping with links, and the
   privacy link from Part 1. The docs page is what telepresence.io renders,
   which satisfies the "homepage and download pages" requirement.
2. **Version resource in the PE files.** SignPath's OSS policies enforce
   metadata restrictions: a signed PE must carry a product name and version
   that match the project. Go emits no `VERSIONINFO` resource, so both exes
   need one. Add a `winres.json` under `cmd/telepresence/` and
   `build-aux/wix-installer/` and run `go-winres make` (pinned in
   `build-aux/tools.mk` like the other tools) before the Windows build, so
   the generated `.syso` is linked in. Product name "Telepresence",
   file description per binary, version from `TELEPRESENCE_VERSION`. This
   also fixes the empty Properties dialog users see today.

   The resource is file metadata read through the Win32 API by Explorer,
   `signtool` and SignPath; the program never reads it. `pkg/version` and
   the `-ldflags -X` value stay the single source of truth for every
   platform and container image, and the Makefile feeds the same
   `TELEPRESENCE_VERSION` to `go-winres`, so the two cannot drift in CI.
   `FILEVERSION` is four 16-bit integers, so `v2.32.0-rc.1` becomes
   `2.32.0.0` there while the string fields carry the full semver. The
   generated `.syso` is gitignored and produced by `build-deps` on Windows
   targets only.
3. **Bundle build split.** Give the WiX Makefile the intermediate targets
   the signing job needs: `msi` (builds `MainPackage.msi` only),
   `bundle` from an existing MSI in `$(MSIDIR)`, `detach-engine` and
   `reattach-engine` wrapping `wix burn detach` and `wix burn reattach`.
   Today `bundle` builds everything in one go, which leaves no hook to
   replace the MSI and engine with signed copies.
4. **Verification target.** `make verify-signatures` on Windows runs
   `signtool verify /pa /all` on every artifact in `build-output/release`
   and fails on any unsigned or untimestamped file. Used by the release job
   and by the release smoke test.

## Part 3: the signing job

A new `sign-windows` job in `.github/workflows/release.yaml`, shaped like
`build-macos-pkg`: runs after `publish-release`, on `windows-latest`,
`environment: windows-signing`, so a reviewer approves the job and SignPath
approvers approve each request. If nobody approves, the release stands with
the unsigned artifacts, as macOS does today.

The Burn format forces three sequential signing rounds, because the engine
can only be extracted from a built bundle and the bundle can only be built
from the already signed MSI. Each round uploads a GitHub Actions artifact and
calls `signpath/github-action-submit-signing-request@v3` with
`wait-for-completion: true`, then downloads the result.

1. **Round 1, `core` artifact configuration.** A zip holding
   `telepresence-windows-amd64/telepresence.exe`,
   `telepresence-windows-arm64/telepresence.exe` and `MainPackage.msi`.
   The configuration deep-signs the two exes inside the MSI, the MSI
   itself, and the two standalone exes.
2. **Round 2, `engine`.** Build the bundle from the signed MSI, detach the
   engine, sign the engine as a single `<pe-file>`.
3. **Round 3, `bundle`.** Reattach the signed engine, sign the bundle as a
   single `<pe-file>`.

Then repack the two zips with the signed `telepresence.exe`, run
`make verify-signatures`, and `gh release upload --clobber` the four files:
`telepresence-windows-amd64.zip`, `telepresence-windows-arm64.zip`,
`telepresence-windows-amd64-setup.exe`, and a new
`telepresence-windows-amd64.msi` for enterprises that deploy MSIs through
Intune or GPO and cannot run a bundle.

Three approvals per release is the price of a Burn bundle. It can be cut to
two by dropping the separate engine signature; the bundle still verifies but
the UAC prompt for repair and uninstall shows "unknown publisher". Recommended:
pay for three; the point of the issue is clean elevation prompts.

The artifact configurations live in the SignPath portal, but their XML is
kept in the repo under `build-aux/signpath/` so changes are reviewed like
code and can be pasted back after portal edits.

Test signing: a `workflow_dispatch` input `signing-policy` defaulting to
`release-signing` lets a maintainer run the job against `test-signing` on a
pre-release tag without approvers, to exercise the pipeline end to end.

## Part 4: documentation and follow-through

- `docs/install/client.md`: how to verify a download
  (`Get-AuthenticodeSignature`, or right-click Properties, Digital
  Signatures) and what the publisher name reads, so the reporter's admins
  can write their allow rule against it.
- `CHANGELOG.yml`: one feature entry, "Windows binaries and installers are
  signed", linking #4284.
- Release smoke test (`test-release` job, Windows leg): add
  `Get-AuthenticodeSignature` status check so a release that slipped
  through unsigned fails loudly.
- Close #4284 with the verification instructions; note SmartScreen
  reputation builds over subsequent releases.

## Sequencing

| Step | Depends on | Owner |
|------|------------|-------|
| 2FA check, teams, privacy pointer, policy section (Parts 1.1 to 1.3, 2.1) | nothing | maintainer + this branch |
| Application (1.4) | policy section published | maintainer |
| Version resources, Makefile split, verify target (2.2 to 2.4) | nothing | this branch, mergeable before approval |
| Portal setup, secret, variables (1.5) | approval | maintainer |
| Signing job, artifact configs, docs, changelog (Parts 3 and 4) | 1.5 for a live test; can be drafted earlier | second PR |

## Out of scope

- Signing the Linux and macOS artifacts differently than today.
- Replacing the zip with winget or Chocolatey packaging (a signed exe
  makes both easier later; separate issue).
- Signing images or Helm charts (cosign), which is a different trust chain.
