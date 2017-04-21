---
layout: doc
weight: 2
title: "Development Info"
categories: additional-information
---

### Releasing Telepresence

Theory of operation:

1. Use [bumpversion](https://pypi.python.org/pypi/bumpversion) to increase the version in relevant files and then commit a new git tag with the new version.
   See `.bumpversion.cfg` for the configuration.
2. Push the new commit and tag to GitHub.
3. This will trigger Travis CI, which will in turn:
   1. Push a new Docker image to the Docker Hub.
   2. Update the Homebrew formula in [homebrew-blackbird](https://github.com/datawire/homebrew-blackbird).
      The Homebrew formula refers to the tarball GitHub [generates for each release](https://github.com/datawire/telepresence/releases).
   3. Upload .deb and .rpm files to packagecloud.io.

The corresponding commands to run in order are:

```
make bumpversion
git push origin master --tags
```
