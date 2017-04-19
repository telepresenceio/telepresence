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

### Linux

First, install the prerequisites:

* On Ubuntu 16.04 or later:

  ```
  apt install --no-install-recommends torsocks python3 openssh-client sshfs
  ```
* On Fedora:

  ```
  dnf install python3 torsocks openssh-clients sshfs
  ```

Then download Telepresence by running the following commands:

```
curl -L https://github.com/datawire/telepresence/raw/{{ site.data.version.version }}/cli/telepresence -o telepresence
chmod +x telepresence
```

Then move telepresence to somewhere in your `$PATH`, e.g.:

```
sudo mv telepresence /usr/local/bin
```
