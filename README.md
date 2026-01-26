# Telepresence: Fast, Local Development for Kubernetes

[<img src="https://raw.githubusercontent.com/telepresenceio/telepresence.io/master/src/assets/images/telepresence-edgy.svg" width="80"/>](https://telepresence.io)

[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/telepresence-oss)](https://artifacthub.io/packages/search?repo=telepresence-oss) [![Gurubase](https://img.shields.io/badge/Gurubase-Ask%20Telepresence%20Guru-006BFF)](https://gurubase.io/g/telepresence)

Telepresence is a [CNCF](https://www.cncf.io/) project that connects your local workstation to a remote Kubernetes cluster, letting you run services locally while accessing cluster resources. It enables fast local development without the container build/push/deploy cycle.

## Key Features

- **Local Development** - Run services on your workstation using your favorite IDE, debugger, and tools
- **Cluster Access** - Your local machine can reach cluster services as if it were inside the cluster
- **Traffic Interception** - Redirect traffic from cluster services to your local machine for debugging
- **Fast Iteration** - No waiting for container builds or deployments

## Getting Started

- [Quick Start Guide](https://telepresence.io/docs/quick-start) - Get up and running in minutes
- [Installation](https://telepresence.io/docs/install/client) - Install the Telepresence client
- [Documentation](https://telepresence.io/docs/) - Full documentation

## How It Works

When Telepresence connects to a cluster, it creates a virtual network interface on your workstation and routes traffic through a Traffic Manager deployed in the cluster. This allows your local services to communicate with cluster resources and optionally intercept traffic destined for cluster workloads.

## Community

- [CNCF Slack](https://communityinviter.com/apps/cloud-native/cncf) - Join [#telepresence-oss](https://cloud-native.slack.com/archives/C06B36KJ85P)
- [Troubleshooting](https://telepresence.io/docs/troubleshooting/) - Common issues and solutions

## Contributing

See [CLAUDE.md](CLAUDE.md) for build instructions, architecture overview, and development guidelines.

## License

Telepresence is licensed under the [Apache License 2.0](LICENSE).
