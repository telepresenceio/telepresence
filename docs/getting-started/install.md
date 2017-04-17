---
layout: doc
weight: 1
title: "Installing Telepresence"
categories: getting-started
permalink: /getting-started/install
---

You will need the following available on your machine:

* OS X or Linux.
* `kubectl` command line tool.
* Access to your Kubernetes cluster, with local credentials on your machine.
  You can do this test by running `kubectl get pod` - if this works you're all set.

You will then need to install the necessary additional dependencies:

* On OS X:

  ```
  brew cask install osxfuse
  brew install python3 torsocks homebrew/fuse/sshfs
  ```
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


> **Need help?** Ask us a question in our [Gitter chatroom](https://gitter.im/datawire/telepresence).
