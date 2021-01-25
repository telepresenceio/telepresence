# Changelog

<!--- towncrier start line -->

#### 0.109 (January 25, 2021)

Bug fixes:

* When using the vpn-tcp method on Linux, the initial `iptables` check will not hang on DNS lookups.
  Thanks to Peter Janes for the patch.
  ([#1476](https://github.com/telepresenceio/telepresence/issues/1476))
* The swap deployment operation no longer fails on Deployments that have startup probes configured.
  Thanks to GitHub user deicon and Anton Troshin for the patch.
  ([#1479](https://github.com/telepresenceio/telepresence/issues/1479))
- No longer provide pre-built packages for Ubuntu 19.10 Eoan Ermine.

#### 0.108 (September 10, 2020)

Bug fixes:

* When swapping a Deployment, Telepresence correctly sets annotations and other metadata in the proxy Pod.
  ([#1430](https://github.com/telepresenceio/telepresence/issues/1430))

#### 0.107 (August 29, 2020)

Features:

* The Telepresence proxy runs in a bare Pod rather than one managed by a Deployment.
  If you experience problems, **please file an issue**, and set the environment variable `TELEPRESENCE_USE_DEPLOYMENT` to any non-empty value to force the old behavior.
  Thanks to Maru Newby and Vladimir Kulev for early bug fixes.
  ([#1041](https://github.com/telepresenceio/telepresence/issues/1041))
* When using the vpn-tcp method with a local cluster, Telepresence now supports resolving additional names via the Telepresence proxy.
  This makes it possible to properly handle DNS resolution for Minikube ingresses.
  Thanks to Vladimir Kulev for the patch.
  ([#1385](https://github.com/telepresenceio/telepresence/issues/1385))

Bug fixes:

* Telepresence now automatically exposes ports defined in the Deployment/DeploymentConfig when using an existing Deployment.
  Telepresence already did this when swapping deployments.
  Thanks to Aslak Knutsen for the patch.
  ([#1356](https://github.com/telepresenceio/telepresence/issues/1356))
* When running on Windows Subsystem for Linux, Telepresence chooses a temporary directory that exists on Debian/Ubuntu WSL installations.
  It falls back to the old value if `/mnt/c` is not available.
  Thanks to Mark Lee for the patch.
  ([#1176](https://github.com/telepresenceio/telepresence/issues/1176))
* Telepresence avoids proxying the external IP of a local cluster when using the vpn-tcp method.
  Thanks to Vladimir Kulev for the patch.
  ([#1380](https://github.com/telepresenceio/telepresence/issues/1380))
* Telepresence avoids generating global networks when guessing Pod and Service networks for the vpn-tcp method.
  Thanks to Israël Hallé for the patch.
  ([#1420](https://github.com/telepresenceio/telepresence/issues/1420))

#### 0.105 (April 24, 2020)

Features:

* The `TELEPRESENCE_USE_OCP_IMAGE` environment variable can be set to `YES` or `NO` to require or disallow use of Telepresence's OCP-specific proxy image, or to `AUTO` to let Telepresence decide as before.
  Thanks to Maru Newby for the patch.
* When performing a swap deployment operation with the container method, host entries are reflected in the local container.
  Thanks to Charlie Catney for the patch.
  ([#1097](https://github.com/telepresenceio/telepresence/issues/1097))
* When using the vpn-tcp method, DNS resolution of names of the form xxx.yyy.zzz.svc is supported.
  This is required to handle Strimzi kafka auto-generated addresses for
  example (see strimzi/strimzi-kafka-operator#2656).
  Thanks to Aurelien Grenotton for the patch.
  ([#560](https://github.com/telepresenceio/telepresence/issues/560))

Bug fixes:

* Telepresence creates new deployments using `kubectl create` rather than `kubectl run`. This allows the new deployment operation to succeed with `kubectl` version 1.18 and later.
  Thanks to Maru Newby for the patch.
  ([#1297](https://github.com/telepresenceio/telepresence/issues/1297))
* The vpn-tcp method uses an even more robust heuristic to determine the Pod IP space.
  Thanks to Maru Newby for the patch.
  ([#1201](https://github.com/telepresenceio/telepresence/issues/1201))

Misc:

* Documentation for using Kubernetes client libaries has been expanded.
  Thanks to Guray Yildirim for the patch.
  ([#1245](https://github.com/telepresenceio/telepresence/issues/1245))
* Telepresence has native packages for Fedora 31 and Ubuntu Eoan.
  Packages for even newer releases will be available once our provider supports them.
  ([#1236](https://github.com/telepresenceio/telepresence/issues/1236))
* Telepresence is no longer packaged for Ubuntu 18.10 (Cosmic Cuttlefish) or Ubuntu 19.04 (Disco Dingo) as those releases have [reached end of life](https://wiki.ubuntu.com/Releases#End_of_Life).

#### 0.104 (January 23, 2020)

Bug fixes:

* Using `--also-proxy` proxies all resolved IPs for a hostname.
  Thanks to Markus Maga for the patch.
  ([#379](https://github.com/telepresenceio/telepresence/issues/379))
* The context specified at the command line is used with all startup operations.
  Thanks to Bret Palsson for the patch.
  ([#1190](https://github.com/telepresenceio/telepresence/issues/1190))
* The vpn-tcp method uses a more robust heuristic to determine the Pod IP space.
  Thanks to Simon Trigona for the patch.
  ([#1201](https://github.com/telepresenceio/telepresence/issues/1201))

#### 0.103 (October 30, 2019)

Backwards incompatible changes:

* Telepresence uses a new OpenShift-specific proxy image when it detects an OpenShift cluster.
  It should no longer be necessary to modify OpenShift cluster policies to allow Telepresence to run.
  The OpenShift proxy image is based on CentOS Linux (instead of Alpine), which means it is significantly larger than the Kubernetes proxy image, so you may notice additional startup latency.
  Use of a CentOS base image should allow for easier approval or certification in some enterprise environments.
  Thanks to GitHub user ReSearchITEng for the patch.
  ([#1114](https://github.com/telepresenceio/telepresence/pull/1114))
* Telepresence uses a new strategy to detect an OpenShift cluster.
  If `openshift.io` is present in the output of `kubectl api-versions`, Telepresence treats the cluster as OpenShift.
  It prefers `oc` over `kubectl` and uses the OpenShift-specific image as above.
  Thanks to Bartosz Majsak for the original patch; blame to the Datawire team for errors in the ultimate implementation.
  ([#1139](https://github.com/telepresenceio/telepresence/issues/1139))

Features:

* Telepresence supports forwarding traffic to and from other containers in the pod. This is useful to connect to proxy/helper containers (`--to-pod`) and to use adapters/agents sending traffic to your app (`--from-pod`).
  ([#728](https://github.com/telepresenceio/telepresence/issues/728))
* When using the `vpn-tcp` method on MacOS, Telepresence will flush the DNS cache once connected.
  This can be useful to clear cached NXDOMAIN responses that were populated when Telepresence was not yet connected.
  Thanks to Karolis Labrencis for the patch.
  ([#1118](https://github.com/telepresenceio/telepresence/issues/1118))

Bug fixes:

* On WSL (Windows 10), Telepresence uses a Docker-accessible directory as the temporary dir.
  Thanks to Shawn Dellysse for the patch.
  ([#1148](https://github.com/telepresenceio/telepresence/issues/1148))
* The connectivity test for the inject-tcp method is able to succeed in more cluster configurations.
  The test hostname is now `kubernetes.default` instead of `kubernetes.default.svc.cluster.local`.
  Thanks to Mohammad Teimori Pabandi for the patch.
  ([#1141](https://github.com/telepresenceio/telepresence/issues/1141))
  ([#1161](https://github.com/telepresenceio/telepresence/issues/1161))

#### 0.102 (October 2, 2019)

Features:

* You can set the Kubernetes service account for new and swapped deployments using the `--serviceaccount` option.
  Thanks to Bill McMath and Dmitry Bazhal for the patches.
  ([#1093](https://github.com/telepresenceio/telepresence/issues/1093))
* When using the container method, you can forward container ports to the host machine.
  This can be useful to allow code running in your container to connect to an IDE or debugger running on the host.
  ([#1022](https://github.com/telepresenceio/telepresence/issues/1022))
* When using the container method, Telepresence can use a Docker volume to mount remote volumes.
  This makes it possible to use volumes even if you don't have mount privileges or capabilities on your main system, e.g. in a container.
  See [the documentation](https://www.telepresence.io/howto/volumes#volume-access-via-docker-volume-for-the-container-method) for more about the new `--docker-mount` feature.
  This is Linux-only for the moment: [#1135](https://github.com/telepresenceio/telepresence/issues/1135).
  Thanks to Sławomir Kwasiborski for the patch.
* When using the default `vpn-tcp` method, you can use the `--local-cluster` flag to bypass local cluster heuristics and force Telepresence to use its DNS loop avoidance workaround.
* Telepresence sets the `command` field when swapping a deployment.
  Thanks to GitHub user netag for the patch.


Bug fixes:

* When using the container method, Telepresence notices if the Docker daemon is not local and reports an error.
  ([#873](https://github.com/telepresenceio/telepresence/issues/873))
* Telepresence is somewhat more robust when working with a local cluster.
  ([#1000](https://github.com/telepresenceio/telepresence/issues/1000))
* Telepresence no longer crashes on `ssh` timeouts.
  ([#1075](https://github.com/telepresenceio/telepresence/issues/1075))
* The CPU limit for the Telepresence pod for new deployments is now 1, fixing performance degradation caused by CPU time throttling.
  See https://github.com/kubernetes/kubernetes/issues/67577 and https://github.com/kubernetes/kubernetes/issues/51135 for more information.
  Thanks to Zhuo Peng for the patch.
  ([#1120](https://github.com/telepresenceio/telepresence/issues/1120))

Misc:

* When using the `inject-tcp` method, Telepresence no longer tries to connect to google.com to check for connectivity.
  Now it tries to connect to kubernetes.default.svc.cluster.local, which should be accessible in common cluster configurations.
  Thanks to GitHub user ReSearchITEng for the patch.
* Telepresence detects an additional name for Docker for Desktop.
  Thanks to William Austin for the patch.

#### 0.101 (June 19, 2019)

Bug fixes:

* Telepresence once again exits when your process finishes.
  ([#1052](https://github.com/telepresenceio/telepresence/issues/1052))
* When using the vpn-tcp method in a container, Telepresence warns you if it is unable to use `iptables` due to missing capabilities, instead of crashing mysteriously much later.
  ([#1054](https://github.com/telepresenceio/telepresence/issues/1054))

#### 0.100 (June 10, 2019)

Features:

* Telepresence can use an OpenShift DeploymentConfig with the `--deployment` option.
  Thanks to Aslak Knutsen for the patch.
  ([#1037](https://github.com/telepresenceio/telepresence/issues/1037))

Bug fixes:

* The unprivileged proxy image switches to the intended UID when unexpectedly running as root.
  This remedies the "unprotected key file" warning from `sshd` and the subsequent proxy pod crash seen by some users.
  ([#1013](https://github.com/telepresenceio/telepresence/issues/1013))
* Attaching a debugger to the process running under Telepresence no longer causes the session to end.
  Thanks to Gigi Sayfan for the patch.
  ([#1003](https://github.com/telepresenceio/telepresence/issues/1003))

Misc:

* If you make a [pull request](https://github.com/telepresenceio/telepresence/pulls) on GitHub, unit tests and linters will run against your PR automatically.
  We hope the quick automated feedback will be helpful.
  Thank you for your contributions!

#### 0.99 (April 17, 2019)

Bug fixes:

* Telepresence correctly forwards privileged ports when using swap-deployment.
  ([#983](https://github.com/telepresenceio/telepresence/issues/983))
* Telepresence once again operates correctly with large clusters.
  ([#981](https://github.com/telepresenceio/telepresence/issues/981))
* Telepresence no longer crashes when the `docker` command requires `sudo`.
  ([#995](https://github.com/telepresenceio/telepresence/issues/995))

Misc:

* Additional timeouts around DNS lookups should make Telepresence startup more reliable when using the default vpn-tcp method.
  ([#986](https://github.com/telepresenceio/telepresence/issues/986))
* When calling `sudo`, Telepresence offers a link to [documentation](https://www.telepresence.io/reference/install#dependencies) about why elevated privileges are required.
  ([#262](https://github.com/telepresenceio/telepresence/issues/262))

#### 0.98 (April 2, 2019)

Features:

* The `TELEPRESENCE_MOUNTS` environment variable contains a list of remote mount points.
  See [the documentation](https://www.telepresence.io/howto/volumes) for more information and example usage.
  Thanks to GitHub user turettn for the patch.
  ([#917](https://github.com/telepresenceio/telepresence/issues/917))

Bug fixes:

* Telepresence no longer crashes when used with kubectl 1.14.
  ([#966](https://github.com/telepresenceio/telepresence/issues/966))
* Telepresence no longer quits if its `kubectl logs` subprocess quits.
  ([#598](https://github.com/telepresenceio/telepresence/issues/598))
* Telepresence waits until a deployment config on OpenShift is successfully rolled back to its original state before proceeding with further cleanup.
  Thanks to Bartosz Majsak for the patch.
  ([#929](https://github.com/telepresenceio/telepresence/issues/929))
* Telepresence tries to detect Kubernetes running with `kind` (kube-in-docker) and work around networking issues the same way as for Minikube.
  Thanks to Rohan Singh for the patch.
  ([#932](https://github.com/telepresenceio/telepresence/issues/932))
* Telepresence accepts private Docker registries as sources for required images when using `TELEPRESENCE_REGISTRY`.
  Thanks to GitHub user arroo for the patch.
  ([#967](https://github.com/telepresenceio/telepresence/issues/967))
* Telepresence's container method supports non-standard cluster DNS search domains.
  Thanks to Loïc Minaudier for the patch.
  ([#940](https://github.com/telepresenceio/telepresence/pull/940))


Misc:

* Telepresence has a native package for the soon-to-be-released Ubuntu Dingo.
* Improved the [Java development documentation](https://www.telepresence.io/tutorials/java#debugging-your-code) with updated Maven debug options for JDK versions 5-8.
  Thanks to Sanha Lee for the patch.
  ([#955](https://github.com/telepresenceio/telepresence/issues/955))

#### 0.97 (January 25, 2019)

Backwards incompatible changes:

* A successful Telepresence session now exits with the return code of your process. This should make it easier to use Telepresence in scripts.
  ([#886](https://github.com/telepresenceio/telepresence/issues/886))

Bug fixes:

* Telepresence should no longer crash if the terminal width is unavailable.
  ([#901](https://github.com/telepresenceio/telepresence/issues/901))
* The container method now outputs the same helpful text about which ports are exposed as the other methods do.
  ([#235](https://github.com/telepresenceio/telepresence/issues/235))
* Telepresence tries to detect Kubernetes in Docker Desktop and work around networking issues the same way as for Minikube.
  Thanks to Rohan Singh for the patch.
  ([#736](https://github.com/telepresenceio/telepresence/issues/736))

Misc:

* Support for OpenShift has been brought up to date.
  Thanks to Bartosz Majsak for the patch.
* Telepresence masks (hides) Kubernetes access tokens in the log file.
  Previously, access tokens would be logged when running in verbose mode.
  Thanks to Bartosz Majsak for the patch.
  ([#889](https://github.com/telepresenceio/telepresence/issues/889))
* Telepresence has native packages for the recently-released Fedora 29 and Ubuntu Cosmic.
  ([#876](https://github.com/telepresenceio/telepresence/issues/876))

#### 0.96 (December 14, 2018)

Bug fixes:

* When using the container method, all outgoing traffic is directed to the cluster.
  It is no longer necessary or meaningful to use `--also-proxy` with `--docker-run`.
  ([#391](https://github.com/telepresenceio/telepresence/issues/391))
* Telepresence shows more information when a background process dies unexpectedly, including the last few lines of output.
  If this happens during startup, the output is included in the crash report.
  ([#842](https://github.com/telepresenceio/telepresence/issues/842))
* Telepresence is less likely to get confused by network setups that have IPv6 enabled.
  ([#783](https://github.com/telepresenceio/telepresence/issues/783))
* Telepresence outputs a warning if cluster and client versions differ greatly.
  ([#426](https://github.com/telepresenceio/telepresence/issues/426))
* Instead of crashing, Telepresence reports an error when
  * the deployment named by `--deployment` does not exist.
    ([#592](https://github.com/telepresenceio/telepresence/issues/592))
  * the deployment named by `--new-deployment` already exists.
    ([#756](https://github.com/telepresenceio/telepresence/issues/756))
  * your command cannot be launched.
    ([#869](https://github.com/telepresenceio/telepresence/issues/869))

#### 0.95 (December 6, 2018)

Bug fixes:

* Telepresence no longer ignores the context argument when checking whether the selected namespace exists.
  ([#787](https://github.com/telepresenceio/telepresence/issues/787))
* Telepresence functions in more restrictive cluster environments because the proxy pod no longer tries to modify the filesystem.
  ([#848](https://github.com/telepresenceio/telepresence/issues/848))
* When a background process dies unexpectedly, Telepresence will notice much sooner.
  This is particularly helpful during session start, as Telepresence can sometimes avoid waiting through a long timeout before crashing.
  ([#590](https://github.com/telepresenceio/telepresence/issues/590))
* Cleanup of background processes is more robust to individual failures.
  ([#586](https://github.com/telepresenceio/telepresence/issues/586))

Misc:

* The container method no longer uses `ifconfig` magic and `socat` to connect the local container to the cluster, relying on `ssh` port forwarding instead.
  If you've had trouble with Telepresence's use of the Docker bridge interface (typically `docker0` on Linux, unavailable on Windows), this change avoids all of that.
  This is technically a breaking change, as ports 38022 and 38023 are used by the new machinery.
  Those ports are now unavailable for user code.
  In practice, most users should not notice a difference.
  ([#726](https://github.com/telepresenceio/telepresence/issues/726))
* The `./build` development script no longer exists.
  Its functionality has been merged into the Makefile.
  See `make help` for the new usage information.
  ([#839](https://github.com/telepresenceio/telepresence/issues/839))


#### 0.94 (November 12, 2018)

Bug fixes:

* Telepresence no longer crashes at launch for OpenShift/MiniShift users. Thanks to Tom Ellis for the patch.
  ([#781](https://github.com/telepresenceio/telepresence/issues/781))

Misc:

* When a new version is available, Telepresence will tell you at the end of the session.
  ([#285](https://github.com/telepresenceio/telepresence/issues/285))

#### 0.93 (October 4, 2018)

Bug fixes:

* Telepresence reports an error message when the specified namespace is not found.
  ([#330](https://github.com/telepresenceio/telepresence/issues/330))
* The container method no longer crashes when no ports are exposed.
  ([#750](https://github.com/telepresenceio/telepresence/issues/750))

Misc:

* Telepresence detects that it is running as root and suggests the user not launch Telepresence under sudo if there is trouble talking to the cluster.
  Thanks to Rohan Gupta for the patch.
  ([#460](https://github.com/telepresenceio/telepresence/issues/460))

#### 0.92 (August 21, 2018)

Bug fixes:

* Fixed the `bash--norc` typo introduced in 0.91.
  ([#738](https://github.com/telepresenceio/telepresence/issues/738))

#### 0.91 (August 17, 2018)

Bug fixes:

* Conntrack, iptables, and a few other dependencies are automatically found on more Linux distributions now.
  ([#278](https://github.com/telepresenceio/telepresence/issues/278))
* Telepresence no longer crashes in the presence of an empty or corrupted cache file.
  ([#713](https://github.com/telepresenceio/telepresence/issues/713))

#### 0.90 (June 12, 2018)

Bug fixes:

* Fixed a regression in the Telepresence executable mode bits in packages.
  ([#682](https://github.com/telepresenceio/telepresence/issues/682))
* Fixed other packaging-related issues.

#### 0.89 (June 11, 2018)

Bug fixes:

* When launching the user's container (when using the container method), if the `docker` command requires `sudo`, Telepresence now uses `sudo -E` to ensure that environment variables get passed through to the container.
  This fixes a regression caused by the [fix for multi-line environment variables (#301)](https://github.com/telepresenceio/telepresence/issues/301).
  ([#672](https://github.com/datawire/telepresence/issues/672))

Misc:

* Version number handling has been simplified.
  ([#641](https://github.com/datawire/telepresence/issues/641))
* Linux packaging has been simplified.
  ([#643](https://github.com/telepresenceio/telepresence/issues/643))

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
