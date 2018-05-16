# Changelog

<!--- towncrier start line -->
#### 0.88 (May 16, 2018)

Features:

* Various points in the Kubernetes stack have timeouts for idle connections.
  This includes the Kubelet, the API server, or even an ELB that might be in front of everything.
  Telepresence now avoids those timeouts by periodically sending data through its open connections.
  In some cases, this will prevent sessions from ending abruptly due to a lost connection.
  ([#573](https://github.com/datawire/telepresence/issues/573))

#### 0.87 (May 11, 2018)

Features:

* Telepresence can optionally emit the remote environment as a JSON blob or as a `.env`-format file.
  Use the `--env-json` or `--env-file` options respectively to specify the desired filenames.
  See [https://docs.docker.com/compose/env-file/](https://docs.docker.com/compose/env-file/) for information about the limitations of the Docker Compose-style `.env` file format.
  ([#608](https://github.com/datawire/telepresence/issues/608))

Bug fixes:

* Telepresence can now transfer complex environment variable values without truncating or mangling them.
  ([#597](https://github.com/datawire/telepresence/issues/597))
* The container method now supports multi-line environment variable values.
  ([#301](https://github.com/datawire/telepresence/issues/301))
* Telepresence avoids running afoul of lifecycle hooks when swapping a deployment.
  ([#587](https://github.com/datawire/telepresence/issues/587))

#### 0.86 (April 26, 2018)

Misc:

* By default, the Telepresence proxy runs in the cluster as an unprivileged user; as built, the proxy image gives up root privileges.
  However, when the proxy needs to bind to privileged ports, it _must_ run as root.
  Instead of using the security context feature of Kubernetes to gain root, Telepresence now uses a different image that does not give up root privileges.
  This allows Telepresence to run in clusters that lock down the Kubernetes security context feature.
  ([#617](https://github.com/datawire/telepresence/issues/617))

#### 0.85 (April 23, 2018)

Features:

* You can set `$TELEPRESENCE_ROOT` to a known path using the `--mount=/known/path` argument.
  See the [volumes documentation](https://www.telepresence.io/howto/volumes) for example usage.
  ([#454](https://github.com/datawire/telepresence/issues/454))
* Turn off volume support entirely with `--mount=false`.
  ([#378](https://github.com/datawire/telepresence/issues/378))

Bug fixes:

* The swap-deployment operation works differently now.

  The original method saved a copy of the deployment manifest, deleted the deployment, and then created a deployment for Telepresence.
  To clean up, it deleted the Telepresence deployment and recreated the original deployment using the saved manifest.
  The problem with this approach was that an outside system managing known deployments could clobber the Telepresence deployment, causing the user's Telepresence session to crash mysteriously.

  The new method creates a separate deployment for Telepresence with the same labels as the original deployment and scales down the original deployment to zero replicas.
  Services will find the new deployment the same way they found the original, via label selectors.
  To clean up, it deletes the Telepresence deployment and scales the original deployment back to its previous replica count.

  An outside system managing known deployments should not touch the Telepresence deployment; it may scale up the original and steal requests from the Telepresence session, but at least that session won't crash mysteriously as it would before.
  ([#575](https://github.com/datawire/telepresence/issues/575))

#### 0.84 (April 20, 2018)

Bug fixes:

* This release fixes startup checks for cluster access to avoid crashing or quitting unnecessarily when sufficient access is available.

#### 0.83 (April 16, 2018)

Misc:

* Telepresence requires fewer cluster permissions than before.
  The required permissions are now [documented](https://www.telepresence.io/reference/connecting#cluster-permissions) for Kubernetes.
  ([#568](https://github.com/datawire/telepresence/issues/568))

#### 0.82 (April 11, 2018)

Bug fixes:

* When using the vpn-tcp method, DNS queries from the domain search path no longer yield NXDOMAIN.
  Unfortunately, the expected follow-up query does not occur under some network conditions.
  This change fixes a DNS regression introduced in 0.79.
  ([#578](https://github.com/datawire/telepresence/issues/578))

#### 0.81 (April 6, 2018)

Bug fixes:

* This release corrects a race condition in subprocess output capture when using the `--verbose` option.

#### 0.79 (April 6, 2018)

Bug fixes:

* Telepresence now supports IPv4 reverse lookups when using `--method=inject-tcp`.
  ([#195](https://github.com/datawire/telepresence/issues/195))
* No more crash when Telepresence cannot write to its log file.
  ([#459](https://github.com/datawire/telepresence/issues/459))
* Fixed remaining instances of logfile content that was not time and origin stamped.
  As a side-effect, the `stamp-telepresence` command has been removed.
  ([#390](https://github.com/datawire/telepresence/issues/390))

Misc:

* The commands that Telepresence launches have always been recorded in the logfile.
  Now they are formatted so they can be copy-pasted into your terminal in most cases.
* The beginning of the logfile contains more information about your local and cluster setup to aid with bug reports and troubleshooting.
* The crash reporter does a better job of capturing relevant information and tries to avoid missing the end of the logfile.
  ([#446](https://github.com/datawire/telepresence/issues/446))
  ([#466](https://github.com/datawire/telepresence/issues/466))
* When using the vpn-tcp method, DNS queries from the domain search path (the search list in `/etc/resolv.conf`) now yield NXDOMAIN instead of implicitly stripping off the search suffix.
  The resolver library will eventually query for the bare name (without the search suffix), at which point Telepresence DNS will return the expected IP address in the cluster.
  ([#192](https://github.com/datawire/telepresence/issues/192))

#### 0.78 (March 29, 2018)

Features:

* Telepresence starts roughly five seconds faster on every invocation thanks to some basic caching of cluster information.
  The cache is stored in `~/.cache/telepresence` and is cleared automatically after twelve hours.
  Delete the contents of that directory to clear the cache manually.

Bug fixes:

* When using the container method, Telepresence waits longer for networking to start before giving up.
  This may help users who sometimes experience higher latency between their local network and their Kubernetes cluster.
  ([#340](https://github.com/datawire/telepresence/issues/340))
  ([#539](https://github.com/datawire/telepresence/issues/539))

#### 0.77 (March 26, 2018)

Misc:

* Updates to the release process.

#### 0.76 (March 25, 2018)

Features:

* Added the ability to specify `--init=false` flag when using `--docker-run`
  Thanks to GitHub user CMajeri for the patch.
  ([#481](https://github.com/datawire/telepresence/issues/481))

Bug fixes:

* Telepresence now makes a greater effort to account for local DNS search domain configuration when bridging DNS to Kubernetes when using `--method=vpn-tcp`.
  ([#393](https://github.com/datawire/telepresence/issues/393))
* Telepresence should no longer get confused looking for the route to the host when using the container method.
  ([#532](https://github.com/datawire/telepresence/issues/532))

Misc:

* A new end-to-end test suite setup will help us reduce the cycle time associated with testing Telepresence. We added documentation introducing developers to Telepresence end-to-end test development.
  ([#400](https://github.com/datawire/telepresence/issues/400))
* Improved cleanup of our testing cluster used by CI.
* Reduced the verbosity of DNS lookup failures in the logs.
  ([#497](https://github.com/datawire/telepresence/issues/497))

#### 0.75 (January 30, 2018)

Bug fixes:

* Telepresence correctly handles the `--publish` (`-p`) Docker option by incorporating it into the `docker` invocation that sets up networking.
  ([#387](https://github.com/datawire/telepresence/issues/387))

Misc:

* The end of startup and the beginning of shutdown are now both clearly indicated in `telepresence.log`.
* Environment and testing setup is no longer entangled with TravisCI setup.
  The `environment-setup.sh` and `build` scripts are used by continuous integration and can be used by developers as well.
  ([#374](https://github.com/datawire/telepresence/issues/374))
* Continuous integration operations, specifically testing, have been moved to CircleCI.
  The release process remains on TravisCI, at least for this release.
  ([#397](https://github.com/datawire/telepresence/issues/397))
  ([#417](https://github.com/datawire/telepresence/issues/417))

#### 0.73 (December 28, 2017)

Features:

* The `--also-proxy` feature supports specifying IP ranges (in [CIDR notation](https://en.wikipedia.org/wiki/Classless_Inter-Domain_Routing#CIDR_notation)) in addition to hostnames and individual IPs.
  ([#375](https://github.com/datawire/telepresence/issues/375))

Misc:

* Telepresence source code is no longer one giant Python file.
  This will allow for quicker development going forward.
  ([#377](https://github.com/datawire/telepresence/pull/377))
* Telepresence source code conforms to `yapf` formatting.
  The lint stage of the CI pipeline enforces this.
  ([#368](https://github.com/datawire/telepresence/issues/368))

#### 0.72 (December 12, 2017)

Misc:

* The Telepresence source tree is organized more like a typical Python project.
  This will allow for quicker development going forward.
  ([#344](https://github.com/datawire/telepresence/pull/344))
* Telepresence has native packages for Ubuntu Xenial, Zesty, and Artful, and for Fedora 26 and 27.
  ([#269](https://github.com/datawire/telepresence/issues/269))
* An install script is included for installing Telepresence from source.
  ([#347](https://github.com/datawire/telepresence/issues/347))

#### 0.71 (November 1, 2017)

Bug fixes:

* Telepresence no longer crashes on deployments containing Services of type ExternalName.
  Thanks to Niko van Meurs for the patch.
  ([#324](https://github.com/datawire/telepresence/pull/324),
  [#329](https://github.com/datawire/telepresence/pull/329))

Misc:

* The [anonymous usage information](usage_reporting) reported by Telepresence now includes the operation (e.g., "swap-deployment") and method (e.g., "vpn-tcp") used.
  This will help us focus development resources.
* Telepresence is no longer packaged for Ubuntu 16.10 (Yakkety Yak) as that release has [reached end of life](http://fridge.ubuntu.com/2017/07/20/ubuntu-16-10-yakkety-yak-end-of-life-reached-on-july-20-2017/).

#### 0.68 (October 12, 2017)

Bug fixes:

* Telepresence no longer crashes when the deployment has multi-line environment variables.
  ([#301](https://github.com/datawire/telepresence/issues/301))
* Telepresence now sets a CPU limit on its Kubernetes pods.
  ([#287](https://github.com/datawire/telepresence/issues/287))
* Deployments that do not use the default service account (and thus don't automatically have access to service account credentials for the k8s API) are now supported.
  Thanks to Dino Hensen for the patch.
  ([#313](https://github.com/datawire/telepresence/pull/313),
  [#314](https://github.com/datawire/telepresence/pull/314))

Misc

* [Telepresence documentation](https://www.telepresence.io/discussion/overview) uses GitBook.

#### 0.67 (September 21, 2017)

Bug fixes:

* The macOS Homebrew installation no longer assumes that you have Homebrew installed in the default location (`/usr/local`). It also no longer requires `virtualenv` to be installed.

Misc:

* The Telepresence logfile now has time and source stamps for almost every line. This will help us diagnose problems going forward.
* Clarified which support binaries are being looked for and where on startup.
* The website now has a [community page](https://www.telepresence.io/reference/community).
* Cleaned up some links (HTTP vs HTTPS, avoid redirection).

#### 0.65 (August 29, 2017)

Bug fixes:

* Avoid a dependency conflict in the macOS Homebrew installation by dropping the required dependency on `socat`. You will still need to install `socat` if you want to use `--method container`, but installing it separately from Telepresence appears to work fine. Thanks to Dylan Scott for chasing this down.
  ([#275](https://github.com/datawire/telepresence/issues/275))

#### 0.64 (August 23, 2017)

Bug fixes:

* Allow `make build-k8s-proxy-minikube` to work on macOS. Same for Minishift.
* Allow `--logfile /dev/null`

Misc:

* Documented macOS limitations with `--method inject-tcp` due to System Integrity Protection. Thanks to Dylan Scott for the detailed write-up.
  ([#268](https://github.com/datawire/telepresence/issues/268))
* The [website](https://www.telepresence.io/) has TLS enabled
* Telepresence [reports anonymous usage information](usage_reporting) during startup

#### 0.63 (July 31, 2017)

Bug fixes:

* Fixed regression in `--swap-deployment` where it would add a proxy container instead of replacing the existing one.
  ([#253](https://github.com/datawire/telepresence/issues/253))

#### 0.62 (July 26, 2017)

Bug fixes:

* Support for Linux distributions using systemd-resolved, like Ubuntu 17.04 and Arch, now works when there is no search domain set.
  Thanks to Vladimir Pouzanov for the bug report, testing, and useful suggestions.
  ([#242](https://github.com/datawire/telepresence/issues/242))
* Better method for bypassing DNS caching on startup, which should be more robust.
* Instead of hardcoding /16, using a better heuristic for guessing the IP range for Services.
  Thanks to Vladimir Pouzanov for the bug report.
  ([#243](https://github.com/datawire/telepresence/issues/243))
* SIGHUP now clean ups resources the same way SIGTERM and hitting Ctrl-C do.
  ([#184](https://github.com/datawire/telepresence/issues/184))

#### 0.61 (July 19, 2017)

Bug fixes:

* Environment variables created using ConfigMaps and Secrets (using `envFrom`) are now made available to the local process.
  Thanks to Tristan Pemble for the bug report.
  ([#230](https://github.com/datawire/telepresence/issues/230))

#### 0.60 (July 18, 2017)

Features:

* When using `--swap-deployment`, ports listed in the existing Deployment are automatically forwarded.
  Thanks to Phil Lombardi and Rafi Schloming for the feature request.
  ([#185](https://github.com/datawire/telepresence/issues/185))

Misc:

* Switched to upstream `sshuttle` instead of using forked version.

#### 0.59 (July 18, 2017)

Bug fixes:

* When using `--swap-deployment`, many more container options that would break `telepresence` are swapped out.
  Thanks to Jonathan Wickens for the bug report.
  ([#226](https://github.com/datawire/telepresence/issues/226))

#### 0.58 (July 13, 2017)

Bug fixes:

* Fixed regression that broke Docker on OS X.
  Thanks to Vincent van der Weele for the bug report.
  ([#221](https://github.com/datawire/telepresence/issues/221))

#### 0.57 (July 6, 2017)

Bug fixes:

* Fix DNS lookups on macOS in `vpn-tcp` mode.
  Thanks to number101010 for the bug report.
  ([#216](https://github.com/datawire/telepresence/issues/216))

#### 0.56 (July 5, 2017)

Features:

* `--help` now includes some examples.
  ([#189](https://github.com/datawire/telepresence/issues/189))

Bug fixes:

* `--docker-run` container no longer gets environment variables from the host, only from the remote pod.
  ([#214](https://github.com/datawire/telepresence/issues/214))

#### 0.55 (June 30, 2017)

Features:

* `--method` is now optional, defaulting to "vpn-tcp", or "container" when `--docker-run` is used.
  ([#206](https://github.com/datawire/telepresence/issues/206))
* If no deployment method (`--new-deployment`, `--swap-deployment` or `--deployment`) then `--new-deployment` is used by default with a randomly generated name.
  ([#170](https://github.com/datawire/telepresence/issues/170))

#### 0.54 (June 28, 2017)

Features:

* `--method vpn-tcp` now works on minikube and minishift.
  As a result we now recommend using it as the default method.
  ([#160](https://github.com/datawire/telepresence/issues/160))

Bug fixes:

* Support more versions of Linux in container mode.
  Thanks to Henri Koski for bug report and patch.
  ([#202](https://github.com/datawire/telepresence/issues/202))

#### 0.53 (June 27, 2017)

Features:

* `--expose` can now expose a different local port than the one used on the cluster side.
  ([#180](https://github.com/datawire/telepresence/issues/180))

Bug fixes:

* Fix regression where exposing ports <1024 stopped working.
  ([#194](https://github.com/datawire/telepresence/issues/194))
* Fix regression where tools like `ping` weren't hidden on Mac in `inject-tcp` method.
  ([#187](https://github.com/datawire/telepresence/issues/187))

#### 0.52 (June 21, 2017)

Features:

* Telepresence can now be used to proxy Docker containers, by using `--method container` together with `--docker-run`.
  Thanks to Iván Montes for the feature request and initial testing.
  ([#175](https://github.com/datawire/telepresence/issues/175))

#### 0.51 (June 13, 2017)

Bug fixes:

* Default `ssh` config is not used, in case it has options that break Telepresence.
  Thanks to KUOKA Yusuke for the bug report, and Iván Montes for debugging and the patch to fix it.
  ([#174](https://github.com/datawire/telepresence/issues/174))

#### 0.50 (June 8, 2017)

Bug fixes:

* If no `current-context` is set in the Kubernetes config, then give a nice
  error message indicating the need for passing `--context` option to
  `telepresence`.
  Thanks to Brandon Philips for the bug report.
  ([#164](https://github.com/datawire/telepresence/issues/164))
* `oc` will not be used unless we're sure we're talking to an OpenShift server. This is useful for Kubernetes users who happen to have a `oc` binary that isn't the OpenShift client.
  Thanks to Brandon Philips for the bug report.
  ([#165](https://github.com/datawire/telepresence/issues/165))

#### 0.49 (June 7, 2017)

Features:

* **Backwards incompatible change:** Telepresence now supports a alternative to `LD_PRELOAD`, a VPN-like connection using [sshuttle](http://sshuttle.readthedocs.io/en/stable/). As a result the `telepresence` command line now has an extra required argument `--method`.
  ([#128](https://github.com/datawire/telepresence/issues/128))
* Added shortcuts for a number of the command line arguments.

#### 0.48 (May 25, 2017)

Bug fixes:

* `--swap-deployment` now works in more cases on OpenShift, in particular when `oc new-app` was used.

#### 0.47 (May 23, 2017)

Features:

* `--swap-deployment` allows replacing an existing Deployment with Telepresence, and then swapping back on exiting the `telepresence` command line.
  ([#9](https://github.com/datawire/telepresence/issues/9))

#### 0.46 (May 16, 2017)

Features:

* Preliminary support for OpenShift Origin.
  Thanks to Eli Young for lots of help figuring out the necessary steps.
  ([#132](https://github.com/datawire/telepresence/issues/132))

Bug fixes:

* Pods created with `--new-deployment` are now looked up using a unique ID, preventing issues where a pod from a previous run was mistakenly used.
  ([#94](https://github.com/datawire/telepresence/issues/94))

#### 0.45 (May 8, 2017)

Bug fixes:

* The Kubernetes-side container used by Telepresence no longer runs as root.
  This will make support for OpenShift Origin easier, as well as other environments that don't want containers running as root.
  Thanks to Eli Young for the patch.
* Increased connection timeout from 3 seconds to 10 seconds, in the hopes of reducing spurious disconnects.
  ([#88](https://github.com/datawire/telepresence/issues/88))
* Common commands that won't work under Telepresence, like `ping` and `nslookup`, will now fail with an appropriate error messages.
  ([#139](https://github.com/datawire/telepresence/issues/139))

#### 0.44 (May 4, 2017)

Bug fixes:

* `telepresence` fails with a better error if a too-old version of Python is used.
   Thanks to Victor Gdalevich for the bug report.
   ([#136](https://github.com/datawire/telepresence/issues/136))
* `telepresence` automatic bug reporting code is triggered by errors during parsing command line arguments.
* If namespace was set using `kubectl config set-context` it will no longer cause Telepresence to break.
  Thanks to spiddy for the bug report.
  ([#133](https://github.com/datawire/telepresence/issues/133))

#### 0.43 (May 3, 2017)

Features:

* `--run` lets you run a command directly as an alternative to running a shell, e.g. `telepresence --new-deployment test --run python3 myapp.py`.
* `telepresence` starts up much faster by polling more frequently and reducing unnecessary sleeps.

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
