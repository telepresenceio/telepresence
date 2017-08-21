---
layout: doc
weight: 3
title: "Usage Reporting"
categories: reference
---

Kubernaut collects some basic information about its users so it can send important client notices such as new version availability and security bulletins. We also use the information to anonymously aggregate basic usage analytics.

### Why?

- We want to know how you are using our software, so we can make it better for you. Knowing what versions are being used, in aggregate, is very helpful for development and testing.
- We ship new releases frequently, with new features and bug fixes. We want you to know when we ship a new release.

### What is collected?

The following information is collected and sent during version checks:

- Application Name ("kubernaut")
- Application Version
- Install Identifier (locally generated for only Kubernaut and stored in `${HOME}/.config/kubernaut/id`)
- Platform Information (Operating System, Python version)

The reporting code can be found in [datawire/scout.py](https://github.com/datawire/scout.py).

### When is it collected?

We collect information during software version checks. We check versions during any command invocation.

### Can it be disabled?

Yes! Set an environment variable `SCOUT_DISABLE=1`.
