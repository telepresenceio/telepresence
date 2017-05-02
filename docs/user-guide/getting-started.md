---
layout: doc
weight: 1
title: "Getting Started"
categories: user-guide
---

<link rel="stylesheet" href="{{ "/css/mermaid.css" | prepend: site.baseurl }}">
<script src="{{ "/js/mermaid.min.js" | prepend: site.baseurl }}"></script>
<script>mermaid.initialize({
   startOnLoad: true,
   cloneCssStyles: false,
 });
</script>

You will need the following available on your machine:

* `kubectl` command line tool.
* Access to your Kubernetes cluster, with local credentials on your machine.
  You can do this test by running `kubectl get pod` - if this works you're all set.

#### OS X

On OS X you can install Telepresence by running the following:

```
brew cask install osxfuse
brew install datawire/blackbird/telepresence
```

#### Ubuntu 16.04 or later

Run the following to install Telepresence:

```
curl -s https://packagecloud.io/install/repositories/datawireio/telepresence/script.deb.sh | sudo bash
sudo apt install --no-install-recommends telepresence
```

#### Fedora 25

Run the following:

```
curl -s https://packagecloud.io/install/repositories/datawireio/telepresence/script.rpm.sh | sudo bash
sudo dnf install telepresence
```

#### Windows

If you are running Windows 10 Creators Edition (released April 2017), you have access to the Windows Subsystem for Linux.
This allows you to run Linux programs transparently inside Windows, with access to the normal Windows filesystem.
Some older versions of Windows also had WSL, but those were based off Ubuntu 14.04 and will not work with Telepresence.

To run Telepresence inside WSL:

1. Install [Windows Subsystem for Linux](https://msdn.microsoft.com/en-us/commandline/wsl/install_guide).
2. Start the BASH.exe program.
3. Install Telepresence by following the Ubuntu instructions above.

Caveats:

* At the moment volumes are not supported on Windows, but [we plan on fixing this](https://github.com/datawire/telepresence/issues/115).
* Network proxying won't affect Windows binaries.
  You can however edit your files in Windows and compile Java or .NET packages, and then run them with the Linux interpreters or VMs.

#### Other platforms

Don't see your favorite platform?
[Let us know](https://github.com/datawire/telepresence/issues/new) and we'll try to add it. 


### Proxying from your local process to Kubernetes

We'll start out by using Telepresence with a newly created Kubernetes `Deployment`, just so it's clearer what is going on.
In the next section we'll discuss using Telepresence with an existing `Deployment` - you can [skip ahead](#using-existing-deployments) if you want.

To get started we'll use `telepresence`'s  `--new-deployment` option, which will create a new `Deployment` and matching `Service`.
The client will connect to the remote Kubernetes cluster via that `Deployment`.
We'll also use the `--run-shell` argument to start a shell that is proxied to the remote Kubernetes cluster.


### Using Telepresence for the first time

**Important:** Note that starting `telepresence` the first time may take a little while, since Kubernetes needs to download the server-side image.

Telepresence proxies networking from your local process to Kubernetes, as well as environment variables and volumes.
To begin with, however, we'll try out another feature: Telepresence allows you to forward traffic from Kubernetes to a local process.

Imagine you're developing a local process.
To simplify the example we'll just use a simple HTTP server:

```console
$ echo "hello from your laptop" > file.txt
$ python3 -m http.server 8081 &
[1] 2324
$ curl http://localhost:8081/file.txt
hello from your laptop
$ kill %1
```

If you only have Python 2 on your computer you can instead do:

```console
$ python2 -m SimpleHTTPServer 8080 &
```

We want to expose this local process so that it gets traffic from Kubernetes.
To do so we need to:

1. Run a Telepresence proxy pod in the remote cluster.
2. Start up the local `telepresence` CLI on your local machine, telling it to run the web server.

First, let's start the Telepresence proxy:

```console
$ kubectl run --port 8080 myserver --image=datawire/telepresence-k8s:{{ data.version.version }}
```

Then we'll expose it to the Internet:

```console
$ kubectl expose deployment myserver --type=LoadBalancer --name=myserver
```

And now we run the local Telepresence client:

```console
$ telepresence --deployment myserver --run python3 -m http.server 8080 &
```

As long as you leave the HTTP server running inside `telepresence` it will be accessible from inside the Kubernetes cluster:

<div class="mermaid">
graph TD
  subgraph Laptop
    code["python HTTP server on port 8080"]---client[Telepresence client]
  end
  subgraph Kubernetes in Cloud
    client-.-proxy["k8s.Pod: Telepresence proxy, listening on port 8080"]
  end
</div>

We can now send queries via the public address of the `Service` we created, and they'll hit the web server running on your laptop:

If your cluster is in the cloud you can find the address of the `Service` like this:

```console
$ kubectl get service myserver
NAME       CLUSTER-IP     EXTERNAL-IP       PORT(S)          AGE
myserver   10.3.242.226   104.197.103.123   8080:30022/TCP   5d
```

In this case it's `http://104.197.103.123:30022`.

On `minikube` you should instead do:

```console
$ minikube service list | grep myserver
| default     | myserver             | http://192.168.99.100:30994 |
```

Once you know the address you can send it a query and it will get routed to your locally running server:

```console
$ curl http://104.197.103.13:30022/file.txt
hello from your laptop
```

Telepresence can do much more than this, of course, which we'll cover in the [next section](/user-guide/features-and-functionality/) of the documentation.
