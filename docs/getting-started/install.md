---
layout: doc
weight: 1
title: "Installing Telepresence"
categories: getting-started
---

You will need the following available on your machine:

* OS X or Linux.
* `kubectl` command line tool.
* Access to your Kubernetes cluster, with local credentials on your machine.
  You can do this test by running `kubectl get pod` - if this works you're all set.

### OS X

On OS X you can install Telepresence by running the following:

```
brew cask install osxfuse
brew install datawire/blackbird/telepresence
```

### Ubuntu 16.04 or later

Run the following to install Telepresence:

```
curl -s https://packagecloud.io/install/repositories/datawireio/telepresence/script.deb.sh | sudo bash
apt install --no-install-recommends telepresence
```

### Fedora 25

Run the following:

```
curl -s https://packagecloud.io/install/repositories/datawireio/telepresence/script.rpm.sh | sudo bash
dnf install telepresence
```

### Other platforms

Don't see your favorite platform?
[Let us know](https://github.com/datawire/telepresence/issues/new) and we'll try to add it. 
