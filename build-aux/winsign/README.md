# Windows signing scripts

- `assemble-release.ps1` repacks the signed `telepresence.exe` into
  `telepresence-windows-<arch>.zip` in `build-output/release`, next to
  the signed MSI.
- `verify-signatures.ps1` is run via `make verify-signatures`. It fails
  on any unsigned or untimestamped `.exe`/`.msi` in `build-output/release`,
  including `telepresence.exe` inside a `.zip`.

`.github/workflows/sign-windows.yaml` runs both scripts.
