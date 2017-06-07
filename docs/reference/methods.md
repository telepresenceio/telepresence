---
layout: doc
weight: 0
title: "Proxying methods"
categories: reference
---

### Choosing a proxying method

Telepresence has two different proxying methods; you will need to choose one of them.

1. `--method inject-tcp` works by injecting a shared library into the subprocess run by Telepresence using `--run` and `--run-shell`.
2. `--method vpn-tcp` works by using a program called [sshuttle](https://shuttle.readthedocs.io) to open a VPN-like connection to the Kubernetes cluster.

Here are some guidelines for choosing which method to use:

* If you are using Go, use `vpn-tcp`.
* If you are using a custom DNS resolver (i.e. not the `gethostbyname` standard API call) use `vpn-tcp`.
* If you are using `minikube`, `minishift`, or otherwise running a local cluster, use `inject-tcp` (we hope to remove this limitation eventually, see [the relevant ticket](https://github.com/datawire/telepresence/issues/160) for details.)
* In all other cases, try `inject-tcp` and if it causes problems switch to `vpn-tcp`.

You can read about the specific limitations of each method below, and read about the differences in what they proxy in the documentation of [what gets proxied](/reference/proxying.html).

### Limitations: `--method vpn-tcp`

In general `--method vpn-tcp` should work with more programs (and programming languages) than `--method inject-tcp`.
For example, if you're developing in Go you'll want to stick to this method.

This method does have some limitations of its own, however:

1. Fully qualified Kubernetes domains like `yourservice.default.svc.cluster.local` won't resolve correctly on Linux.
   `yourservice` and `yourservice.default` will resolve correctly, however.
   See [the relevant ticket](https://github.com/datawire/telepresence/issues/161) for details.
2. `minikube`, `minishift` and other ways of running Kubernetes on your local machine won't work: non-Kubernetes DNS lookups will fail.
   See [the relevant ticket](https://github.com/datawire/telepresence/issues/160) for details.
3. Only one instance of `telepresence` should be running at a time on any given developer machine.
4. Cloud resources like AWS RDS will not be routed automatically via cluster.
   You'll need to specify the hosts manually using `--also-proxy`, e.g. `--also-proxy mydatabase.somewhere.vpc.aws.amazon.com` to route traffic to that host via the Kubernetes cluster..

### Limitations: `--method inject-tcp`

If you're using `--method inject-tcp` you will have certain limitations.

#### Incompatible programs

Because of the mechanism Telepresence uses to intercept networking calls when using `inject-tcp`:

* suid binaries won't work inside a Telepresence shell.
* Statically linked binaries won't work.
* Custom DNS resolvers that parse `/etc/resolv.conf` and do DNS lookups themselves won't work.

Thus command line tools like `ping`, `nslookup`, `dig`, `host` and `traceroute` won't work either because they do lower-level DNS or are suid.

However, this only impacts outgoing connections.
Incoming proxying (from Kubernetes) will still work with these binaries.

#### Golang

Programs written with the Go programming language will not work by default with this method.
We recommend using `--method vpn-tcp` instead if you're writing Go, since that method will work with Go.

`--method inject-tcp` relies on injecting a shared library into processes you run, and Go uses a custom system call implementation and has its own DNS resolver.
This causes connections *to* Kubernetes not to work.
On OS X many Go programs won't start all, including `kubectl`.

If you don't want to use `--method vpn-tcp` for some reason you can also workaround these limitations by doing the following in your development environment (there is no need to change anything for production):

* Use `gccgo` instead of `go build`.
* Do `export GODEBUG=netdns=cgo` to [force Go to use the standard DNS lookup mechanism](https://golang.org/pkg/net/#hdr-Name_Resolution) rather than its own internal one.

But the easiest thing to do, again, is to use `--method vpn-tcp` while *will* work with Go.
