# [Documentation](https://telepresence.io) - start here!

[![Build Status](https://circleci.com/gh/telepresenceio/telepresence.svg?style=shield)](https://circleci.com/gh/telepresenceio/workflows)
[![Join the chat at https://d6e.co/slack](https://img.shields.io/badge/chat-on%20Slack-blue.svg)](https://d6e.co/slack)
[![CII Best Practices](https://bestpractices.coreinfrastructure.org/projects/1863/badge)](https://bestpractices.coreinfrastructure.org/projects/1863)

## Demo

[![asciicast](https://asciinema.org/a/117761.png)](https://asciinema.org/a/117761)

## Telepresence: fast, realistic local development for Kubernetes microservices

Have you ever wanted the quick development cycle of local code while still having your code run within a remote Kubernetes cluster?
Telepresence allows you to run your code locally while still:

1. Giving your code access to Services in a remote Kubernetes cluster.
2. Giving your code access to cloud resources like AWS RDS or Google PubSub.
3. Allowing Kubernetes to access your code as if it were in a normal pod within the cluster.

## Quick Start

1. [Install locally](https://www.telepresence.io/reference/install) with Homebrew, apt, or dnf.

2. Run `telepresence`.

3. You now have a shell that proxies connections to Kubernetes.

For more about Telepresence, and the various options, [read the documentation](https://www.telepresence.io/discussion/overview).

## Usage Reporting

Telepresence collects some basic information about its users so it can send important client notices, such as new version availability and security bulletins. We also use the information to aggregate basic usage analytics anonymously. To disable this behavior set the environment variable `SCOUT_DISABLE`:

    export SCOUT_DISABLE=1

To know more, check the [documentation](https://www.telepresence.io/reference/usage_reporting) on usage reporting.

## About Telepresence

Telepresence is an open source project hosted by the [Cloud Native Computing Foundation](https://www.cncf.io) and originally created by [Datawire](https://www.datawire.io). Telepresence is licensed under the Apache 2.0 License. For information about recent releases, see https://www.telepresence.io/reference/changelog.
