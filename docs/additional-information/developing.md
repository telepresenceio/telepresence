---
layout: doc
weight: 2
title: "Development Info"
categories: additional-information
---

### Releasing Telepresence

You will need:

1. Commit access to the https://github.com/datawire/telepresence/ repository.
2. Commit access to the https://github.com/datawire/homebrew-blackbird/ repository.
3. Ability to push to the `datawire` organization in the Docker Hub.

Theory of operation:

1. Use [bumpversion](https://pypi.python.org/pypi/bumpversion) to increase the version in relevant files and then commit a new git tag with the new version.
   See `.bumpversion.cfg` for the configuration.
2. Push the new commit and tag to GitHub.
3. Finally, push a new Docker image to the Docker Hub and update the Homebrew formula in [homebrew-blackbird](https://github.com/datawire/homebrew-blackbird).
   The Homebrew formula refers to the tarball GitHub [generates for each release](https://github.com/datawire/telepresence/releases).

The corresponding commands to run in order are:

```
make bumpversion
git push origin master --tags
make release
```

Note that the release automation has only been tested on Ubuntu 16.04.
