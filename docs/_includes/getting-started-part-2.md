Once you know the address you can send it a query and it will get routed to your locally running server:

```console
$ curl http://104.197.103.13:8080/file.txt
Hello, world!
```

#### Swapping your deployment with Telepresence

**Important:** Starting `telepresence` the first time may take a little while, since {{ include.cluster }} needs to download the server-side image.

At this point you want to switch to developing the service locally, replace the version running on your cluster with a custom version running on your laptop.
To simplify the example we'll just use a simple HTTP server:

```console
$ mkdir /tmp/telepresence-test
$ cd /tmp/telepresence-test
$ echo "hello from your laptop" > file.txt
$ python3 -m http.server 8081 &
[1] 2324
$ curl http://localhost:8081/file.txt
hello from your laptop
$ kill %1
```

We want to expose this local process so that it gets traffic from {{ include.cluster }}, replacing the existing `hello-world` deployment.

**Important:** you're about to expose a web server on your laptop to the Internet.
This is pretty cool, but also pretty dangerous!
Make sure there are no files in the current directory that you don't want shared with the whole world.

Here's how you should run `telepresence`:



```console
$ telepresence --swap-deployment hello-world --expose 8000 \
               --run python3 -m http.server 8080 &
```

This will:

* Replace the existing `hello-world` pod with one running the Telepresence proxy. On exit, the old pod will be restored.
* Run the local web server and hook it up to the networking proxy.

As long as you leave the HTTP server running inside `telepresence` it will be accessible from inside the {{ include.cluster }} cluster:

<div class="mermaid">
graph TD
  subgraph Laptop
    code["python HTTP server on port 8080"]---client[Telepresence client]
  end
  subgraph {{ include.cluster }} in Cloud
    client-.-proxy["k8s.Pod: Telepresence proxy, listening on port 8080"]
  end
</div>

We can now send queries via the public address of the `Service` we created, and they'll hit the web server running on your laptop instead of the original code that was running there before:

```console
$ curl http://104.197.103.13:8080/file.txt
hello from your laptop
```

Finally, let's kill Telepresence locally so you don't have to worry about other people accessing your local web server:

```console
$ fg
telepresence --deployment myserver --expose 8080 --run python3 -m http.server 8080
^C
Keyboard interrupt received, exiting.
```

Now if we wait a few seconds the old code will be swapped back in:

```console
$ curl http://104.197.103.13:8080/file.txt
Hello, world!
```

Now it's time to clean up the service:

```console
$ {{ cluster.command }} delete {{ cluster.deployment | lower }},service hello-world
```

Telepresence can do much more than this: see the reference section of the documentation, on the left, for details.
