# CLAUDE.md

This file provides guidance for contributors and AI assistants working with this repository.

## Project Overview

Telepresence is a Kubernetes development tool that enables fast local development by connecting your local workstation to a Kubernetes cluster. It allows developers to run services locally while accessing cluster resources and intercepting traffic from the cluster to their local machine.

## Git Workflow

- Never commit directly to the `release/v2` branch. Always create a feature branch with a name following the pattern `username/topic` (e.g., `thallgren/fix-dns-resolution`).
- All commits must be signed and signed-off (`git commit -s -S`).
- Push the branch and create a pull request for review.
- Always merge PRs with a merge commit (never squash or rebase).

## Build Artifacts

The Open Source version of Telepresence consists of three artifacts:

**Client-side (runs on developer workstation):**
- **`telepresence` binary** - The same binary serves as CLI, user daemon, and root daemon.
- **`telepresence` Docker image** - Used as both user and root daemon when running `telepresence connect --docker`.

**Cluster-side (runs in Kubernetes):**
- **`tel2` Docker image** - Used by the traffic-manager deployment and injected as traffic-agent sidecars.

## Build Commands

```bash
# Set required environment variables
export TELEPRESENCE_VERSION=v2.x.x-alpha.0  # or use auto-generated version
export TELEPRESENCE_REGISTRY=local          # 'local' for Docker Desktop, or 'ghcr.io/telepresenceio'

# Build the telepresence binary
make build

# Build Docker images (for local Kubernetes development)
make client-image    # Client container image
make tel2-image      # Traffic-manager/traffic-agent image

# Build everything for local development
make build client-image tel2-image

# Install to system
make install

# Clean build artifacts
make clean
make clobber  # Also removes tools
```

Environment variables:
- `TELEPRESENCE_REGISTRY` (required) - Docker registry for images. Use `local` for docker-based Kubernetes, or `ghcr.io/telepresenceio` for the release registry.
- `TELEPRESENCE_VERSION` (optional) - Version string to compile into binaries and images. If not set, auto-generated from CHANGELOG.yml and source hash.

Run `make help` for more information.

### Building on Windows

Windows builds use `build-aux\winmake.bat` instead of `make` directly. Pass the same parameters as you would to make. The script runs make inside a Docker container with appropriate parameters for Windows binaries.

## Testing

```bash
# Run unit tests
make check-unit

# Run all integration tests (requires Kubernetes cluster)
make check-integration

# Run a single integration test
go test ./integration_test/... -v -testify.m=Test_InterceptDetailedOutput

# Run an integration test suite
TEST_SUITE='^WorkloadConfiguration$' go test ./integration_test/... -v

# Build tests without running (useful for caching)
make build-tests
```

Integration tests use testify suites. The test harness is in `integration_test/itest/`. Use `-testify.m=<pattern>` to filter tests by name. Verbose output (`-v`) is recommended as tests produce human-readable output with timestamps that correlate with log files.

### Integration Test Environment Variables

| Environment Name           | Description                                   | Default                   |
|----------------------------|-----------------------------------------------|---------------------------|
| `DEV_KUBECONFIG`           | Cluster configuration used by the tests       | Kubernetes default        |
| `DEV_CLIENT_REGISTRY`      | Docker registry for the client image          | ${TELEPRESENCE_REGISTRY}  |
| `DEV_MANAGER_REGISTRY`     | Docker registry for the traffic-manager image | ${TELEPRESENCE_REGISTRY}  |
| `DEV_AGENT_REGISTRY`       | Docker registry for the traffic-agent image   | Traffic-manager registry  |
| `DEV_CLIENT_IMAGE`         | Name of the client image                      | "telepresence"            |
| `DEV_MANAGER_IMAGE`        | Name of the traffic-manager image             | "tel2"                    |
| `DEV_AGENT_IMAGE`          | Name of the traffic-agent image               | Traffic-manager image     |
| `DEV_CLIENT_VERSION`       | Client version                                | ${TELEPRESENCE_VERSION#v} |
| `DEV_MANAGER_VERSION`      | Traffic-manager version                       | ${TELEPRESENCE_VERSION#v} |
| `DEV_AGENT_VERSION`        | Traffic-agent image version                   | Traffic-manager version   |
| `DEV_USERD_PROFILING_PORT` | Start user daemon with pprof enabled          |                           |
| `DEV_ROOTD_PROFILING_PORT` | Start root daemon with pprof enabled          |                           |
| `TEST_SUITE`               | Regexp matching test suite name(s)            |                           |

These can also be provided in an `itest.yml` file placed next to `config.yml`:

```yaml
Env:
  DEV_CLIENT_VERSION: v2.x.x-alpha.0
  DEV_KUBECONFIG: /path/to/kubeconfig
Config:
  docker:
    addHostGateway: false
```

### Using Docker Desktop with Kubernetes

Using Kubernetes bundled with Docker Desktop is the quickest way to run tests. No need to push images to a registry - Kubernetes finds them in Docker's local cache. Integration tests automatically use `pullPolicy=Never` when `DEV_CLIENT_REGISTRY` is set to "local".

```bash
export TELEPRESENCE_VERSION=v2.x.x-alpha.0
export TELEPRESENCE_REGISTRY=local
make build client-image tel2-image
go test ./integration_test/... -v -testify.m=Test_InterceptDetailedOutput
```

## Linting

```bash
# Run all linters
make lint

# Run Go linter only
make lint-go

# Run protobuf linter only
make lint-rpc

# Auto-fix lint issues
make format
```

Linting uses golangci-lint v2 running in Docker. Configuration is in `.golangci.yml`.

## Code Generation

```bash
# Regenerate protobuf and license files
make generate

# Regenerate protobuf files only
make protoc

# Regenerate documentation files (after changing CHANGELOG.yml)
make docs-files
```

**Important:** After modifying `CHANGELOG.yml`, always run `make docs-files` to regenerate documentation files (`docs/release-notes.md`, `docs/release-notes.mdx`, `docs/variables.yml`).

**Important:** All files under `docs/reference/cli/` are generated from Go source code. Do not edit them directly; instead, modify the corresponding Go source and regenerate.

### Updating License Documentation

Run `make generate` and commit changes to `DEPENDENCY_LICENSES.md` and `DEPENDENCIES.md`.

## Architecture

### Main Components

1. **CLI/Client** (`cmd/telepresence/`, `pkg/client/cli/`)
   - Single binary serving as CLI, user daemon, and root daemon
   - Commands are in `pkg/client/cli/cmd/`

2. **User Daemon (userd)** (`pkg/client/userd/`)
   - Runs as the user, manages connection to traffic-manager
   - Handles intercepts, port forwards, cluster communication

3. **Root Daemon (rootd)** (`pkg/client/rootd/`)
   - Runs with elevated privileges
   - Manages virtual network interface (VIF) and DNS

4. **Traffic Manager** (`cmd/traffic/cmd/manager/`)
   - Runs in the Kubernetes cluster (ambassador namespace by default)
   - Coordinates intercepts between clients and traffic-agents

5. **Traffic Agent** (`cmd/traffic/cmd/agent/`)
   - Injected as sidecar into intercepted pods
   - Routes traffic between the pod and the local machine

6. **Agent Init** (`cmd/traffic/cmd/agentinit/`)
   - Init container for setting up iptables rules in pods

7. **Docker Network Driver** (`cmd/teleroute/`)
   - Only used when connecting with `--docker` flag
   - Provides the Docker network that enables communication between the Telepresence daemon container and other containers

### Key Packages

- `pkg/vif/` - Virtual network interface implementation
- `pkg/tunnel/` - gRPC-based tunneling for network traffic
- `pkg/dnsproxy/` - DNS resolution and proxying
- `pkg/agentconfig/` - Traffic-agent configuration
- `pkg/client/k8s/` - Kubernetes client interactions
- `pkg/routing/` - Network routing logic
- `pkg/client/cli/cmd/` - CLI commands. One per file.

### RPC Definitions

Protocol buffers are in `rpc/` with separate packages:
- `rpc/connector/` - Client-to-userd communication
- `rpc/daemon/` - Client-to-rootd communication
- `rpc/manager/` - Client/userd-to-traffic-manager communication
- `rpc/agent/` - Traffic-manager-to-traffic-agent communication

### Helm Chart

The traffic-manager Helm chart is in `charts/telepresence-oss/`.

## Debugging and Troubleshooting

### Log Files

There are three log files:
- `connector.log` - Output from user daemon: traffic-manager interaction, intercepts, port forwards
- `daemon.log` - Output from root daemon: networking changes on your workstation
- `cli.log` - Output from the command line interface

Locations:
- macOS: `~/Library/Logs/telepresence/`
- Linux: `~/.cache/telepresence/logs/`
- Windows: `%USERPROFILE%\AppData\Local\logs`

Logs rotate daily. Use `tail -F <filename>` to watch rotating logs seamlessly.

### Debugging Early-Initialization Errors

If daemons fail during early initialization before logfiles are set up, run them directly to see stderr output. The `--address` flag is mandatory:

```bash
# Run user daemon directly
telepresence userd --logfile - --address :8083

# Run root daemon directly (requires sudo)
sudo telepresence rootd --logfile - --address :8084
```

### Profiling the Daemons

Enable [pprof](https://pkg.go.dev/net/http/pprof) profiling:

```bash
telepresence quit -s
telepresence connect --userd-profiling-port 6060 --rootd-profiling-port 6061
# Then browse http://localhost:6060/debug/pprof/
```

### Dumping Goroutine Stacks

Send SIGQUIT to a daemon to dump goroutine stacks to its log file. On Windows, use profiling instead.

### RBAC Testing

To test with limited RBAC privileges:

```bash
kubectl apply -f k8s/client_rbac.yaml
kubectl get sa telepresence-test-developer -o "jsonpath={.secrets[0].name}"
# Get the token from the secret and configure kubectl
kubectl get secret <secret-name> -o "jsonpath={.data.token}" | base64 --decode
kubectl config set-credentials telepresence-test-developer --token <token>
kubectl config use-context telepresence-test-developer
```

## Releases

To create a release, set `TELEPRESENCE_VERSION` and run `make prepare-release`. This creates two annotated tags (`vX.Y.Z` and `rpc/vX.Y.Z`) and a commit updating go.mod references. Pushing the tags and branch triggers the release workflow.

```bash
# Test release (marked as pre-release, not promoted to latest)
export TELEPRESENCE_VERSION=v2.27.0-test.0
make prepare-release
git push origin HEAD $TELEPRESENCE_VERSION rpc/$TELEPRESENCE_VERSION

# Release candidate
export TELEPRESENCE_VERSION=v2.27.0-rc.0
make prepare-release
git push origin HEAD $TELEPRESENCE_VERSION rpc/$TELEPRESENCE_VERSION

# GA release (becomes "latest", updates Homebrew)
export TELEPRESENCE_VERSION=v2.27.0
make prepare-release
git push origin HEAD $TELEPRESENCE_VERSION rpc/$TELEPRESENCE_VERSION
```

Version formats:
- `vX.Y.Z-test.N` - Test release (pre-release)
- `vX.Y.Z-rc.N` - Release candidate (pre-release)
- `vX.Y.Z` - GA release (marked as latest, triggers Homebrew update)

### Changelog

When adding entries to `CHANGELOG.yml` for an upcoming release:
- Use `date: (TBD)` for unreleased versions
- The `make prepare-release` command will set the actual date when `TELEPRESENCE_VERSION` is a GA version (e.g., `v2.27.0`)
- After modifying `CHANGELOG.yml`, run `make docs-files` to regenerate documentation

### Documentation Website

The documentation website at [telepresence.io](https://telepresence.io) is managed in the [telepresenceio/telepresence.io](https://github.com/telepresenceio/telepresence.io) repository. When creating a GA release, update the website by running `make generate-version` in that repository with:
- `DOCS_BRANCH` - Branch in this repository containing the docs (e.g., `release/v2`)
- `DOCS_VERSION` - Major.minor version to generate or update (e.g., `2.27`)

See the telepresence.io repository for full instructions.

### macOS Installer Signing and Notarization

The macOS `.pkg` installers are signed and notarized to pass Gatekeeper verification. The signing process uses a protected GitHub Environment to secure the signing credentials.

#### Environment Setup

The `build-macos-pkg` job uses the `macos-signing` environment, which must be configured in the repository settings:

1. Go to https://github.com/telepresenceio/telepresence/settings/environments
2. Create an environment named `macos-signing`
3. Enable "Required reviewers" and add authorized personnel
4. Optionally restrict deployment branches to `release/*`
5. Add the following secrets to the environment (not repository-level):

| Secret Name | Description |
|-------------|-------------|
| `MACOS_CERTIFICATE_P12` | Base64-encoded P12 file containing both Application and Developer ID Installer certificates |
| `MACOS_CERTIFICATE_PASSWORD` | Password for the P12 file |
| `MACOS_SIGN_APPLICATION` | Developer ID Application certificate name (e.g., `Developer ID Application: Your Name (TEAMID)`) |
| `MACOS_SIGN_INSTALLER` | Developer ID Installer certificate name (e.g., `Developer ID Installer: Your Name (TEAMID)`) |
| `MACOS_NOTARIZE_APPLE_ID` | Apple ID email for notarization |
| `MACOS_NOTARIZE_TEAM_ID` | Apple Developer Team ID |
| `MACOS_NOTARIZE_PASSWORD` | App-specific password for notarization |

#### Release Workflow

When a release tag is pushed:
1. All platform binaries (Linux, Windows, macOS) are built immediately
2. Linux `.deb`/`.rpm` and Windows `.exe` installers are built
3. The release is published with all binaries and Linux/Windows installers
4. The `build-macos-pkg` job waits for approval from a required reviewer
5. Once approved, signed `.pkg` installers are built and added to the release

This design ensures:
- **Emergency releases can proceed** without the signing approver being available (all binaries and Linux/Windows installers are released)
- **Signing credentials are protected** by requiring explicit approval before they are exposed
- **Signed packages are added later** when the approver reviews and approves the job

If the environment is not configured or never approved, the release will contain macOS standalone binaries but not `.pkg` installers.

#### Obtaining the Certificates

You need an [Apple Developer Program](https://developer.apple.com/programs/) membership ($99/year) to obtain signing certificates.

1. **Create certificates in Apple Developer Portal:**
   - Go to [Certificates, Identifiers & Profiles](https://developer.apple.com/account/resources/certificates/list)
   - Click the + button to create a new certificate
   - Create **Developer ID Application** certificate (for signing binaries)
   - Create **Developer ID Installer** certificate (for signing .pkg files)
   - Download both certificates and double-click to install in Keychain Access

2. **Find your Team ID:**
   - Go to [Membership Details](https://developer.apple.com/account#MembershipDetailsCard)
   - Copy the Team ID (10-character alphanumeric string)
   - Set as `MACOS_NOTARIZE_TEAM_ID`

3. **Find the certificate names:**
   - Open Keychain Access and look under "My Certificates"
   - The names will be like:
     - `Developer ID Application: Your Name (TEAMID)` → `MACOS_SIGN_APPLICATION`
     - `Developer ID Installer: Your Name (TEAMID)` → `MACOS_SIGN_INSTALLER`
   - You can also list them with: `security find-identity -v -p codesigning`

4. **Export certificates to P12:**
   ```bash
   # Export each certificate from Keychain Access:
   # - Right-click certificate → Export
   # - Choose .p12 format
   # - Set a strong password (will be MACOS_CERTIFICATE_PASSWORD)

   # If you have both in separate .p12 files, you can import them together
   # or export them together from Keychain Access by selecting both

   # Base64-encode for GitHub secrets:
   base64 -i certificates.p12 | pbcopy
   # Paste as MACOS_CERTIFICATE_P12
   ```

5. **Create app-specific password for notarization:**
   - Go to [appleid.apple.com](https://appleid.apple.com/) → Sign-In and Security → App-Specific Passwords
   - Generate a new password with a descriptive name (e.g., "GitHub Actions Notarization")
   - Copy the generated password → `MACOS_NOTARIZE_PASSWORD`
   - Use your Apple ID email → `MACOS_NOTARIZE_APPLE_ID`

#### Testing Locally

To test signing locally before configuring GitHub secrets:

```bash
# Set environment variables
export MACOS_SIGN_APPLICATION="Developer ID Application: Your Name (TEAMID)"
export MACOS_SIGN_INSTALLER="Developer ID Installer: Your Name (TEAMID)"
export MACOS_NOTARIZE_APPLE_ID="your@email.com"
export MACOS_NOTARIZE_TEAM_ID="ABCD123456"
export MACOS_NOTARIZE_PASSWORD="xxxx-xxxx-xxxx-xxxx"

# Build the signed and notarized package
cd build-aux/pkg-installer
VERSION=2.26.0 ./build-pkg.sh

# Verify the signature
pkgutil --check-signature ../../build-output/Telepresence.pkg
spctl --assess --type install ../../build-output/Telepresence.pkg
```
