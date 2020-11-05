# Fast development workflow with Docker and Kubernetes

Keeping development environments in sync is a constant pain. Containerizing your development environment enables your service to run in the exact same environment everywhere, from your laptop to production (for more details on the benefits of a container native development workflow, see [this post by Matt Butcher](https://open.microsoft.com/2018/04/23/5-reasons-you-should-be-doing-container-native-development/).)

Telepresence, in conjunction with a containerized development environment, gives the developer a fast development workflow in developing a multi-container application on Kubernetes.  Telepresence lets you run a Docker container locally while proxying it to your Kubernetes cluster.

In this HOWTO, we'll walk through how to use Telepresence with a containerized Docker environment to build a fast development workflow.

{% import "../macros.html" as macros %}
{{ macros.install("https://kubernetes.io/docs/tasks/tools/install-kubectl/", "kubectl", "Kubernetes", "top") }}

## Quick example

We'll start with a quick example. Apply [this manifest](https://github.com/telepresenceio/telepresence/blob/master/docs/tutorials/hello-world.yaml)
to create a deployment and service both named hello-world, exposed on port 8000.
Then confirm that the deployment becomes ready:

```console
$ kubectl apply -f https://raw.githubusercontent.com/telepresenceio/telepresence/master/docs/tutorials/hello-world.yaml
deployment.apps/hello-world created
service/hello-world created

$ kubectl get deployments
NAME                          READY   UP-TO-DATE   AVAILABLE   AGE
deployment.apps/hello-world   1/1     1            1           6s
```

It may take a minute or two for the pod running the server to be up and running,
depending on how fast your cluster is.

You can now run a Docker container using Telepresence that can access that 
service, even though the process is local but the service is running in the 
Kubernetes cluster:

```console
$ telepresence --docker-run --rm -it pstauffer/curl curl http://hello-world:8000/
[...]
T: Setup complete. Launching your container.
Hello, world!
T: Your process has exited.
[...]
```


## Setting up a development environment in Docker

So how would we use Telepresence to do actual *development* of the hello-world service? We'll set up a local Dockerized development environment for hello-world. Clone the hello-world repo:

```console
$ git clone https://github.com/datawire/hello-world
Cloning into 'hello-world'...
[...]
$ cd hello-world
```

In the repository is a [`Dockerfile`](https://github.com/datawire/hello-world/blob/master/Dockerfile) that builds a runtime environment for the hello-world service.

Build the runtime environment and tag it `hello-dev`:

```console
$ docker build -t hello-dev .
Sending build context to Docker daemon  24.58kB
Step 1/7 : FROM python:3-alpine
 ---> a93594ce93e7
[...]
 ---> 7d692d619894
Successfully built 7d692d619894
Successfully tagged hello-dev:latest
```

We'll use Telepresence to swap the hello-world deployment with the local Docker image. Behind the scenes, Telepresence invokes `docker run`, so it supports any arguments you can pass to `docker run`. In this case, we're going to also mount our local directory to `/usr/src/app` in your Docker container. Make sure your current working directory is the `hello-world` directory, since we're going to mount that directly into the container.

```console
$ telepresence --swap-deployment hello-world --docker-run --rm -it -v $(pwd):/usr/src/app hello-dev
T: Volumes are rooted at $TELEPRESENCE_ROOT. See https://telepresence.io/howto/volumes.html for details.
T: Starting network proxy to cluster by swapping out Deployment hello-world with a proxy
T: Forwarding remote port 8000 to local port 8000.

T: Setup complete. Launching your container.
 * Serving Flask app "server" (lazy loading)
[...]
```

We can test this out. In another terminal, we'll start a pod remotely on the Kubernetes cluster.

```console
$ kubectl run curler -it --rm --image=pstauffer/curl --restart=Never -- sh
If you don't see a command prompt, try pressing enter.
/ # curl http://hello-world:8000
Hello, world!
/ #
```

Let's change the message in `server.py`. At a shell prompt in the `hello-world` directory, modify the file using `sed`:

```console
$ sed -i.bak -e s/Hello/Greetings/ server.py
[no output]
```

or just use your editor to change the file. The change we have made is very simple:

```console
$ git diff
diff --git a/server.py b/server.py
index 04f15e2..7fffeb1 100644
--- a/server.py
+++ b/server.py
@@ -1,7 +1,7 @@
 from flask import Flask

 PORT = 8000
-MESSAGE = "Hello, world!\n"
+MESSAGE = "Greetings, world!\n"

 app = Flask(__name__)

```

Rerun the `curl` command from your remote pod:

```console
/ # curl http://hello-world:8000
Greetings, world!
/ #
```

Notice how the output has updated in realtime. Congratulations! You've now:

* Routed the hello-world service to the Docker container running locally
* Configured your Docker service to pick up changes from your local filesystem
* Made a live code edit and seen it immediately reflected in production

## How it works

Telepresence will start a new proxy container and then call `docker run` with whatever arguments you pass to `--docker-run` to start a container that will have its networking proxied. All networking is proxied:

* Outgoing to Kubernetes.
* Outgoing to cloud resources outside the cluster
* Incoming connections from the cluster to ports specified with `--expose`.

Volumes and environment variables from the remote `Deployment` are also available in the container.

## Cleaning up and next step

* Quit your remote pod shell (`exit`) to clean up that pod.
* Press Ctrl-C at your Telepresence terminal. Telepresence will swap the deployment back to its original state.
* In a real development situation, you would commit your development work and let CI do its thing. Or build and deploy your changes however you normally would.

{{ macros.install("https://kubernetes.io/docs/tasks/tools/install-kubectl/", "kubectl", "Kubernetes", "bottom") }}

{{ macros.tutorialFooter(page.title, file.path, book['baseUrl']) }}
