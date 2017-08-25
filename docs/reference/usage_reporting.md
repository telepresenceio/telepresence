---
layout: doc
weight: 8
title: "Usage Reporting"
categories: reference
---

Telepresence collects some basic information about its users so it can send important client notices, such as new version availability and security bulletins.
We also use the information to aggregate basic usage analytics anonymously.

### Why?

- We want to know how you are using our software, so we can make it better for you.
  Knowing what versions are being used, in aggregate, is very helpful for development and testing.
- We ship new releases frequently, with new features and bug fixes.
  We want you to know when we ship a new release.

### What is reported?

The following information is collected and sent during version checks:

- Application name ("telepresence")
- Application version
- Installation identifier (locally generated for only Telepresence and stored in `~/.config/telepresence/id`)
- Platform information (operating system, Python version)

The reporting code can be found in [`cli/telepresence`][1] by searching for "Usage".

[1]: https://github.com/datawire/telepresence/blob/master/cli/telepresence

### When is it reported?

Telepresence collects and reports usage every time a session is launched.

### Can it be disabled?

Yes. Set the environment variable `SCOUT_DISABLE`.

    export SCOUT_DISABLE=1
