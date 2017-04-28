---
layout: doc
weight: 1
title: "Changelog"
categories: additional-information
---

#### 0.42 (April 28, 2017)

Bug fixes:

* `~/.bashrc` is no longer loaded by the Telepresence shell, to ensure it doesn't break when e.g. `kubectl` is run there. Thanks to discopalevo for the bug report.
  ([#126](https://github.com/datawire/telepresence/issues/126))
* Log files are written to original path, not wherever you happen to `cd` to.
  ([#120](https://github.com/datawire/telepresence/issues/120))
* Better error messages when a custom Deployment is missing or misconfigured.
  ([#121](https://github.com/datawire/telepresence/issues/121))

#### 0.41 (April 26, 2017)

Features:

* Telepresence can run on Windows using the Windows Subsystem for Linux.

Bug fixes:

* Telepresence now sets a RAM limit on its Kubernetes pods.
* Telepresence Kubernetes pod exits faster.

Releases 0.31 to 0.40 were spent debugging release automation.

#### 0.30 (April 19, 2017)

Features:

* Telepresence can now be installed via Homebrew on OS X.

#### 0.29 (April 13, 2017)

Bug fixes:

* Fix surprising error about `umount` when shutting down on Linux.

#### 0.28 (April 13, 2017)

Features:

* Remote volumes are now accessible by the local process.
  ([#78](https://github.com/datawire/telepresence/issues/78))

#### 0.27 (April 12, 2017)

Features:

* `--context` option allows choosing a `kubectl` context.
  Thanks to Svend Sorenson for the patch.
  ([#3](https://github.com/datawire/telepresence/issues/3))

Bug fixes:

* Telepresence no longer breaks if compression is enabled in `~/.ssh/config`.
  Thanks to Svend Sorenson for the bug report.
  ([#97](https://github.com/datawire/telepresence/issues/97))

#### 0.26 (April 6, 2017)

Backwards incompatible changes:

* New requirements: openssh client and Python 3 must be installed for Telepresence to work.
  Docker is no longer required.

Features:

* Docker is no longer required to run Telepresence.
  ([#78](https://github.com/datawire/telepresence/issues/78))
* Local servers just have to listen on localhost (127.0.0.1) in order to be accessible to Kubernetes; previously they had to listen on all interfaces.
  ([#77](https://github.com/datawire/telepresence/issues/77))

0.25 failed the release process due to some sort of mysterious mistake.

#### 0.24 (April 5, 2017)

Bug fixes:

* The `KUBECONFIG` environment variable will now be respected, so long as it points at a path inside your home directory.
  ([#84](https://github.com/datawire/telepresence/issues/84))
* Errors on startup are noticed, fixing issues with hanging indefinitely in the "Starting proxy..." phase.
  ([#83](https://github.com/datawire/telepresence/issues/83))

#### 0.23 (April 3, 2017)

Bug fixes:

* Telepresence no longer uses lots of CPU busy-looping.
  Thanks to Jean-Paul Calderone for the bug report.

#### 0.22 (March 30, 2017)

Features:

* Telepresence can now interact with any Kubernetes namespace, not just the default one.
  ([#74](https://github.com/datawire/telepresence/issues/74))

Backwards incompatible changes:

* Running Docker containers locally (`--docker-run`) is no longer supported.
  This feature will be reintroduced in the future, with a different implementation, if there is user interest.
  [Add comments here](https://github.com/datawire/telepresence/issues/76) if you're interested.

#### 0.21 (March 28, 2017)
  
Bug fixes:

* Telepresence exits when connection is lost to the Kubernetes cluster, rather than hanging.
* Telepresence notices when the proxy container exits and shuts down.
  ([#24](https://github.com/datawire/telepresence/issues/24))

#### 0.20 (March 27, 2017)

Bug fixes:

* Telepresence only copies environment variables explicitly configured in the `Deployment`, rather than copying all environment variables.
* If there is more than one container Telepresence copies the environment variables from the one running the `datawire/telepresence-k8s` image, rather than the first one.
  ([#38](https://github.com/datawire/telepresence/issues/38))

#### 0.19 (March 24, 2017)

Bug fixes:

* Fixed another issue with `--run-shell` on OS X.

#### 0.18 (March 24, 2017)

Features:

* Support `--run-shell` on OS X, allowing local processes to be proxied.
* Kubernetes-side Docker image is now smaller.
  ([#61](https://github.com/datawire/telepresence/issues/61))

Bug fixes:
  
* When using `--run-shell`, allow access to the local host.
  Thanks to Jean-Paul Calderone for the bug report.
  ([#58](https://github.com/datawire/telepresence/issues/58))

#### 0.17 (March 21, 2017)

Bug fixes:

* Fix problem with tmux and wrapping when using `--run-shell`.
  Thanks to Jean-Paul Calderone for the bug report.
  ([#51](https://github.com/datawire/telepresence/issues/51))
* Fix problem with non-login shells, e.g. with gnome-terminal.
  Thanks to Jean-Paul Calderone for the bug report.
  ([#52](https://github.com/datawire/telepresence/issues/52))
* Use the Deployment's namespace, not the Deployment's spec namespace since that may not have a namespace set.
  Thanks to Jean-Paul Calderone for the patch.
* Hide torsocks messages.
  Thanks to Jean-Paul Calderone for the bug report.
  ([#50](https://github.com/datawire/telepresence/issues/50))

#### 0.16 (March 20, 2017)

Bug fixes:

* Disable `--run-shell` on OS X, hopefully temporarily, since it has issues with System Integrity Protection.
* Fix Python 3 support for running `telepresence`.

#### 0.14 (March 20, 2017)

Features:

* Added `--run-shell`, which allows proxying against local processes.
  ([#1](https://github.com/datawire/telepresence/issues/1))

#### 0.13 (March 16, 2017)

Bug fixes:

* Increase time out for pods to start up; sometimes it takes more than 30 seconds due to time to download image.

#### 0.12 (March 16, 2017)

Bug fixes:

* Better way to find matching pod for a Deployment.
  ([#43](https://github.com/datawire/telepresence/issues/43))

#### 0.11 (March 16, 2017)

Bug fixes:

* Fixed race condition that impacted `--expose`.
  ([#40](https://github.com/datawire/telepresence/issues/40))

#### 0.10 (March 15, 2017)

Bug fixes:

* Fixed race condition the first time Telepresence is run against a cluster.
  ([#33](https://github.com/datawire/telepresence/issues/33))

#### 0.9 (March 15, 2017)

Features:

* Telepresence now detects unsupported Docker configurations and complain.
  ([#26](https://github.com/datawire/telepresence/issues/26))
* Better logging from Docker processes, for easier debugging.
  ([#29](https://github.com/datawire/telepresence/issues/29))

Bug fixes:

* Fix problem on OS X where Telepresence failed to work due to inability to share default location of temporary files.
  ([#25](https://github.com/datawire/telepresence/issues/25))

#### 0.8 (March 14, 2017)

Features:

* Basic logging of what Telepresence is doing, for easier debugging.
* Check for Kubernetes and Docker on startup, so problems are caught earlier.
* Better error reporting on crashes. ([#19](https://github.com/datawire/telepresence/issues/19))

Bug fixes:

* Fixed bug where combination of `--rm` and `--detach` broke Telepresence on versions of Docker older than 1.13. Thanks to Jean-Paul Calderone for reporting the problem. ([#18](https://github.com/datawire/telepresence/issues/18))
