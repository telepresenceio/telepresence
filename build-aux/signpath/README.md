# SignPath artifact configurations

These files mirror the artifact configurations kept in the SignPath
portal, so changes to what gets signed are reviewed like code. When
editing a configuration in the portal, paste the result back here in the
same change.

- `core.xml` — slug `core`. Signs `telepresence.exe` (amd64, arm64) and
  both `telepresence-windows-amd64.msi` and `telepresence-windows-arm64.msi`,
  deep-signing the two exes each MSI embeds.
- `verify-signatures.ps1` — run via `make verify-signatures`; fails on any
  unsigned or untimestamped `.exe`/`.msi` in `build-output/release`,
  including `telepresence.exe` inside a `.zip`.
- `assemble-release.ps1` — repacks the signed exe into the release zips
  and stages the signed MSIs into `build-output/release`.

`.github/workflows/sign-windows.yaml` submits signing requests against
these slugs, under policy slug `release-signing` (manual approval) or
`test-signing` (no approval, for dry runs).
