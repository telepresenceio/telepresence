# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Telepresence is a Kubernetes development tool that enables fast local development by connecting your local workstation to a Kubernetes cluster. It allows developers to run services locally while accessing cluster resources and intercepting traffic from the cluster to their local machine.

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

Integration tests use testify suites. The test harness is in `integration_test/itest/`. Use `-testify.m=<pattern>` to filter tests by name.

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
```

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

### Key Packages

- `pkg/vif/` - Virtual network interface implementation
- `pkg/tunnel/` - gRPC-based tunneling for network traffic
- `pkg/dnsproxy/` - DNS resolution and proxying
- `pkg/agentconfig/` - Traffic-agent configuration
- `pkg/client/k8s/` - Kubernetes client interactions
- `pkg/routing/` - Network routing logic

### RPC Definitions

Protocol buffers are in `rpc/` with separate packages:
- `rpc/connector/` - Client-to-userd communication
- `rpc/daemon/` - Client-to-rootd communication
- `rpc/manager/` - Client/userd-to-traffic-manager communication
- `rpc/agent/` - Traffic-manager-to-traffic-agent communication

### Helm Chart

The traffic-manager Helm chart is in `charts/telepresence-oss/`.

## Local Development with Docker Desktop

For fastest iteration when using Docker Desktop with Kubernetes:

```bash
export TELEPRESENCE_VERSION=v2.x.x-alpha.0
export TELEPRESENCE_REGISTRY=local
make build client-image tel2-image

# Install traffic-manager with debug logging
./build-output/bin/telepresence helm install --set logLevel=debug,image.pullPolicy=Never,agent.image.pullPolicy=Never

# Connect to cluster
./build-output/bin/telepresence connect
```

## Integration Test Configuration

Tests can be configured via environment variables or `itest.yml` file placed next to `config.yml`:

```yaml
Env:
  DEV_CLIENT_VERSION: v2.x.x-alpha.0
  DEV_KUBECONFIG: /path/to/kubeconfig
Config:
  docker:
    addHostGateway: false
```

Key environment variables:
- `DEV_KUBECONFIG` - Kubernetes config for tests
- `DEV_CLIENT_REGISTRY`, `DEV_MANAGER_REGISTRY`, `DEV_AGENT_REGISTRY` - Image registries
- `TEST_SUITE` - Regexp to filter test suites

## Log Files

- macOS: `~/Library/Logs/telepresence/`
- Linux: `~/.cache/telepresence/logs/`
- Windows: `%USERPROFILE%\AppData\Local\logs`

Files: `daemon.log` (rootd), `connector.log` (userd), `cli.log` (CLI)

## Debugging Daemons

```bash
# Run daemon with stderr logging
telepresence userd --logfile -
telepresence rootd --logfile -

# Enable pprof profiling
telepresence connect --userd-profiling-port 6060 --rootd-profiling-port 6061
# Then browse http://localhost:6060/debug/pprof/
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
