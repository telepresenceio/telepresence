---
title: Quick start
description: "Start using Telepresence in your own environment. Follow these steps to intercept your service in your cluster."
hide_table_of_contents: true
---

# Telepresence Quickstart

Telepresence is an open source tool that enables you to set up remote development environments for Kubernetes where you can still use all of your favorite local tools like IDEs, debuggers, and profilers.

## Prerequisites

- [kubectl](https://kubernetes.io/docs/tasks/tools/install-kubectl/), the Kubernetes command-line tool, or the OpenShift Container Platform command-line interface, [oc](https://docs.openshift.com/container-platform/4.2/cli_reference/openshift_cli/getting-started-cli.html#cli-installing-cli_cli-developer-commands).
- A Kubernetes Deployment and Service.

## Install Telepresence

Follow [Install Client](install/client.md) and [Install Traffic Manager](install/manager.md) instructions to install the
telepresence client on your workstation, and the traffic manager in your cluster.

Checkout the [Howto](howtos/attach.md) to learn how Telepresence can attach to resources in your remote cluster,
enabling you to run the code on your local workstation.

## What’s Next?
- [Learn about the Telepresence architecture.](reference/architecture)
