Once you know the address you can store its value (don't forget to replace this with the real address!):

```console
$ export HELLOWORLD=http://104.197.103.13:8000
```

And you send it a query and it will be served by the code running in your cluster:

```console
$ curl $HELLOWORLD/
Hello, world!
```

#### Swapping your deployment with Telepresence

**Important:** Starting `telepresence` the first time may take a little while, since {{ include.cluster }} needs to download the server-side image.

At this point you want to switch to developing the service locally, replace the version running on your cluster with a custom version running on your laptop.
To simplify the example we'll just use a simple HTTP server that will run locally on your laptop:

```console
$ mkdir /tmp/telepresence-test
$ cd /tmp/telepresence-test
$ echo "hello from your laptop" > file.txt
$ python3 -m http.server 8001 &
[1] 2324
$ curl http://localhost:8001/file.txt
hello from your laptop
$ kill %1
```

We want to expose this local process so that it gets traffic from {{ include.cluster }}, replacing the existing `hello-world` deployment.

**Important:** you're about to expose a web server on your laptop to the Internet.
This is pretty cool, but also pretty dangerous!
Make sure there are no files in the current directory that you don't want shared with the whole world.

Here's how you should run `telepresence` (you should make sure you're still in the `/tmp/telepresence-test` directory you created above):


```console
$ cd /tmp/telepresence-test
$ telepresence --swap-deployment hello-world --expose 8000 \
               --run python3 -m http.server 8000 &
```

This does two things:

* `--swap-deployment` tells Telepresence to replace the existing `hello-world` pod with one running the Telepresence proxy. On exit, the old pod will be restored.
* `--run` tells Telepresence to run the local web server and hook it up to the networking proxy.

As long as you leave the HTTP server running inside `telepresence` it will be accessible from inside the {{ include.cluster }} cluster.
You've gone from this...

<div class="mermaid">
graph RL
  subgraph {{ include.cluster }} in Cloud
    server["datawire/hello-world server on port 8000"]
  end
</div>

...to this:

<div class="mermaid">
graph RL
  subgraph Laptop
    code["python HTTP server on port 8000"]---client[Telepresence client]
  end
  subgraph {{ include.cluster }} in Cloud
    client-.-proxy["Telepresence proxy, listening on port 8000"]
  end
</div>

We can now send queries via the public address of the `Service` we created, and they'll hit the web server running on your laptop instead of the original code that was running there before.
Wait a few seconds for the Telepresence proxy to startup; you can check its status by doing:

```console
$ {{ include.command }} get pod | grep hello-world
hello-world-2169952455-874dd   1/1       Running       0          1m
hello-world-3842688117-0bzzv   1/1       Terminating   0          4m
```

Once you see that the new pod is in `Running` state you can use the new proxy to connect to the web server on your laptop:

```console
$ curl $HELLOWORLD/file.txt
hello from your laptop
```

Finally, let's kill Telepresence locally so you don't have to worry about other people accessing your local web server by bringing it to the background and hitting Ctrl-C:

```console
$ fg
telepresence --swap-deployment hello-world --expose 8000 --run python3 -m http.server 8000
^C
Keyboard interrupt received, exiting.
```

Now if we wait a few seconds the old code will be swapped back in.
Again, you can check status of swap back by running:

```console
$ {{ include.command }} get pod | grep hello-world
```

When the new pod is back to `Running` state you can see that everything is back to normal:

```console
$ curl $HELLOWORLD/file.txt
Hello, world!
```

<hr>

> **What you've learned:** Telepresence lets you replace an existing deployment with a proxy that reroutes traffic to a local process on your machine.
> This allows you to easily debug issues by running your code locally, while still giving your local process full access to your staging or testing cluster.

<hr> 

Now it's time to clean up the service:
