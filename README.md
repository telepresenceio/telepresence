# [Documentation](https://telepresence.io) - start here!

[![Build Status](https://circleci.com/gh/telepresenceio/telepresence.svg?style=shield)](https://circleci.com/gh/telepresenceio/workflows)
[![Join the chat at https://d6e.co/slack](https://img.shields.io/badge/chat-on%20Slack-blue.svg)](https://d6e.co/slack)
[![CII Best Practices](https://bestpractices.coreinfrastructure.org/projects/1863/badge)](https://bestpractices.coreinfrastructure.org/projects/1863)

## Demo

[![asciicast](https://asciinema.org/a/117761.png)](https://asciinema.org/a/117761)

## Telepresence: fast, efficient local development for Kubernetes microservices

Telepresence gives developers infinite scale development environments for Kubernetes. With Telepresence:

* You run one service locally, using your favorite IDE and other tools
* You run the rest of your application in the cloud, where there is unlimited memory and compute

This gives developers:

* a fast local dev loop, with no waiting for a container build / push / deploy
* ability to use their favorite local tools (IDE, debugger, etc.)
* ability to run large-scale applications that can't run locally

## Quick Start

1. [Install locally](https://www.telepresence.io/reference/install) with Homebrew, apt, or dnf.

2. Run `telepresence`.

3. You now have a shell that proxies connections to Kubernetes.

For more about Telepresence, and the various options, [read the documentation](https://www.telepresence.io/discussion/overview).

## Usage Reporting

Telepresence collects some basic information about its users so it can send important client notices, such as new version availability and security bulletins. We also use the information to aggregate basic usage analytics anonymously. To disable this behavior set the environment variable `SCOUT_DISABLE`:

    export SCOUT_DISABLE=1

To know more, check the [documentation](https://www.telepresence.io/reference/usage_reporting) on usage reporting.

## Get Involved

* Follow [@telepresenceio](https://twitter.com/telepresenceio) on Twitter
* Join the [Telepresence Slack](https://d6e.co/slack)

## About Telepresence

Telepresence is an open source project hosted by the [Cloud Native Computing Foundation](https://www.cncf.io) and originally created by [Ambassador Labs](https://www.getambassador.io). Telepresence is licensed under the Apache 2.0 License. For information about recent releases, see https://www.telepresence.io/reference/changelog. Ambassador Labs also provides commercial support for a version of Telepresence that is [designed for teams](https://www.getambassador.io/use-case/local-kubernetes-development/).
