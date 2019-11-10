# Proxying methods

## Choosing a proxying method

Telepresence has four different proxying methods; you will need to choose one of them.

1. `--method inject-tcp` works by injecting a shared library into the subprocess run by Telepresence using `--run` and `--run-shell`.
2. `--method vpn-tcp` works by using a program called [sshuttle](https://sshuttle.readthedocs.io) to open a VPN-like connection to the Kubernetes cluster.
3. `--method teleproxy` is an experimental approach that uses Teleproxy (FIXME: link) in place of sshuttle and has a different set of tradeoffs, but essentially behaves similarly to `vpn-tcp`.
3. `--method container` (documented in the [Docker tutorial](/tutorials/docker.html)) launches a Docker container with its network proxied to the cluster.

If you are comfortable developing in Docker, the container method offers a reliable way to get an environment very similar to running in Kubernetes directly.
Multiple simultaneous sessions work robustly without interfering with one another.
We recommend using the container method whenever it is applicable.

For local processes instead of containers, `vpn-tcp` should work in more cases than `inject-tcp`, and it is chosen by default (unless `--docker-run` is used, in which case the `container` method is the default.)

If you want to run more than one Telepresence connection per machine, use `inject-tcp` or try the experimental `teleproxy` method (or use the container method).

If you don't want proxying to affect all processes, use `inject-tcp` (or use the container method).

You can read about the specific limitations of each method below, and read about the differences in what they proxy in the documentation of [what gets proxied](/reference/proxying.html).

## Limitations: `--method vpn-tcp`

`--method vpn-tcp` should work with more programs (and programming languages) than `--method inject-tcp`.
For example, if you're developing in Go you'll want to stick to this method.

This method does have some limitations of its own, however:

* Fully qualified Kubernetes domains like `yourservice.default.svc.cluster.local` won't resolve correctly on Linux.
  `yourservice` and `yourservice.default` will resolve correctly, however.
  See [the relevant ticket](https://github.com/datawire/telepresence/issues/161) for details.
* Only one instance of `telepresence` using `vpn-tcp` should be running at a time on any given developer machine. Running other
  instances with other proxying methods concurrently is possible.
* VPNs may interfere with `telepresence`, and vice-versa: don't use both at once.
* Cloud resources like AWS RDS will not be routed automatically via cluster.
  You'll need to specify the hosts manually using `--also-proxy`, e.g. `--also-proxy mydatabase.somewhere.vpc.aws.amazon.com` to route traffic to that host via the Kubernetes cluster.

## Limitations: `--method teleproxy`

`--method teleproxy` works much like  `--method vpn-tcp`, but is still experimental.

This method's limitations are a little different:

* Fully qualified Kubernetes domains like `yourservice.default.svc.cluster.local` won't resolve correctly on Linux.
  `yourservice` and `yourservice.default` will resolve correctly, however.
  See [the relevant ticket](https://github.com/datawire/telepresence/issues/161) for details.
* Multiple instances of `telepresence` using `teleproxy` connecting to the same cluster and namespace works fine as long as you run `edgectl connect` manually before starting your `telepresence` sessions.
* VPNs may interfere with `telepresence`, and vice-versa: don't use both at once.
* Cloud resources like AWS RDS will not be routed automatically via cluster.
  Support for adding them manually using `--also-proxy` is coming soon.

## Limitations: `--method inject-tcp`

If you're using `--method inject-tcp` you will have certain limitations.

### Incompatible programs

Because of the mechanism Telepresence uses to intercept networking calls when using `inject-tcp`:

* suid binaries won't work inside a Telepresence shell.
* Statically linked binaries won't work.
* Custom DNS resolvers that parse `/etc/resolv.conf` and do DNS lookups themselves won't work.

Thus command line tools like `ping`, `nslookup`, `dig`, `host` and `traceroute` won't work either because they do lower-level DNS or are suid.

However, this only impacts outgoing connections.
Incoming proxying (from Kubernetes) will still work with these binaries.

### Golang

Programs written with the Go programming language will not work by default with this method.
We recommend using `--method vpn-tcp` instead if you're writing Go, since that method will work with Go.

`--method inject-tcp` relies on injecting a shared library into processes you run, and Go uses a custom system call implementation and has its own DNS resolver.
This causes connections *to* Kubernetes not to work.
On OS X many Go programs won't start all, including `kubectl`.

If you don't want to use `--method vpn-tcp` for some reason you can also work around these limitations by doing the following in your development environment (there is no need to change anything for production):

* Use `gccgo` instead of `go build`.
* Do `export GODEBUG=netdns=cgo` to [force Go to use the standard DNS lookup mechanism](https://golang.org/pkg/net/#hdr-Name_Resolution) rather than its own internal one.

But the easiest thing to do, again, is to use `--method vpn-tcp`, which *does* work with Go.

### MacOS System Integrity Protection

In OS X El Capitan (10.11), Apple introduced a security feature called System Integrity Protection (SIP).

* [Apple's _About SIP on your Mac_ article](https://support.apple.com/en-us/HT204899)
* [Apple's SIP guide for developers](https://developer.apple.com/library/content/documentation/Security/Conceptual/System_Integrity_Protection_Guide/Introduction/Introduction.html#//apple_ref/doc/uid/TP40016462-CH1-DontLinkElementID_15)
* [Wikipedia article about SIP](https://en.wikipedia.org/wiki/System_Integrity_Protection)

SIP prevents, among other things, code injection into processes that originate from certain designated "protected directories" (including `/usr` and `/bin`). This includes [purging dynamic linker environment variables](https://developer.apple.com/library/content/documentation/Security/Conceptual/System_Integrity_Protection_Guide/RuntimeProtections/RuntimeProtections.html) for these processes. These protections are in place even when running as root. They can only be disabled by booting into recovery mode, and disabling them is highly discouraged.

Telepresence attempts to work around SIP to some extent by creating duplicates of `/bin`, `/usr/bin`, etc. and putting those in the `PATH` instead of the SIP-protected originals. This allows the user to type something like `env ENABLE_FROBULATE=1 ./my_binary` and get the benefits of `inject-tcp`; the `env` binary comes from an unprotected location in `/tmp`.

What _does not_ work is using the full path to `/bin/sh` or `/usr/bin/env` or similar, since Telepresence cannot manipulate those commands when located in those directories. In practice, avoiding protected binaries is difficult because it is common for tools and scripts to use `sh` or `env` by full path, thereby losing Telepresence's injected libraries. As a result, connections _to_ Kubernetes do not work.

Carefully avoiding protected binaries is the only reliable workaround. One hackish approach would be to create a directory tree containing copies of the binaries in the protected directories in a stable location (e.g., `~/bin_copy`). That would allow changing all tools and scripts to use those unprotected copies; this would have to be done in production as well as on your development machine. It would be much easier to use `--method vpn-tcp` instead.

See [issue 268](https://github.com/datawire/telepresence/issues/268) for one user's experience.
