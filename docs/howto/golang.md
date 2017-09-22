# Go support

In this howto you'll learn how to use Telepresence with a Go program.
Because of the way Go is implemented you will need to use `--method vpn-tcp` with `telepresence`.

### A Go program talking to Kubernetes

First, start a web server inside Kubernetes:

```console
$ kubectl run hello-world --image=datawire/hello-world --port=8000 --expose
```

Next, install a neat little Go program called [wuzz](https://github.com/asciimoo/wuzz), an interactive HTTP client.

```console
$ go get github.com/asciimoo/wuzz
```

Now we'll see how we can use wuzz to interact with a remote Kubernetes cluster.
`telepresence` will create a new `Deployment` inside Kubernetes that will act as a proxy, and then communication from the `wuzz` subprocess it runs will be forwarded to the cluster:

```console
$ telepresence --run $GOPATH/bin/wuzz http://hello-world:8000/
```

**Important:** Go programs will *not* work with `--method inject-tcp` option.

The `wuzz` UI will appear with the URL `http://hello-world:8000/`.
Hit Enter and you should see the "Hello, World!" response from the Kubernetes service.
You can also interact with the Kubernetes API - change the URL to `https://kubernetes/` (but typically you'll have problems with the custom certificate authority.)

### Kubernetes talks to a Go program

You can also run a Go program as a local server and have requests to your Kubernetes `Deployment` forwarded to that process.
This is just the same as the example covered in [the tutorial](/tutorials/kubernetes.html) except that you use `--method vpn-tcp`, and run a Go process instead of a Python process.

For example, if you have a `Deployment` called `myservice` running in Kubernetes and listening on port 8080, you can temporarily swap it out for a local process and have traffic forwarded to your laptop:

```console
$ telepresence --swap-deployment myservice --expose 8080 \
               --run ./yourgoserver --port=8080
```

Now requests to that remote `Deployment` will be routed to the `yourgoserver` process running on your machine.

You can learn more about the differences between `--new-deployment` and `--swap-deployment` in the relevant [reference documentation](/reference/connecting.html).
