# Local Connection to Kubernetes Client Libraries
*Author: Guray Yildirim ([@gurayyildirim](https://twitter.com/gurayyildirim))*

{% import "../macros.html" as macros %}
{{ macros.install("https://kubernetes.io/docs/tasks/tools/install-kubectl/", "kubectl", "Kubernetes", "top") }}

### Intro

Kubernetes has client libraries in many different languages. It is not rare to have situations that require connecting Kubernetes API from your cluster and getting resources/creating new pods & deployments, ... While the list goes on, Kubernetes provide ServiceAccount objects in its RBAC to fill up this need. Still, development from local computers, testing, and debugging become a pain due to lack of direct access to the cluster API using token.

Using Telepresence, it becomes an easy task to access ServiceAccount token seamlessly with your libraries. Here are the links for jumping:

- [Java Kubernetes Client Local Connection](#java-kubernetes-client)
- [Python Kubernetes Client Local Connection](#python-kubernetes-client)

### Java Kubernetes Client

If you are using a Kubernetes client like this [one](https://github.com/fabric8io/kubernetes-client), you need to make sure the client can access service account information. This can be done with the `--mount` command introduced in `Telepresence 0.85`.

We need to add the following to the command:

* `--mount /tmp/known` Tells `Telepresence` to mount `TELEPRESENCE_ROOT` to a known folder
* `-v=/tmp/known/var/run/secrets:/var/run/secrets` This is another Docker mounting command to mount the known folder to `/var/run/secrets` in the local container. The [Fabric8 Kubernetes client](https://github.com/fabric8io/kubernetes-client) can find the secrets there as it would inside Kubernetes

So our `telepresense.sh` file would look like that

> telepresence.sh
> ```bash
> telepresence --mount /tmp/known --swap-deployment foo --docker-run --rm -v$(pwd):/build -v $HOME/.m2/repository:/m2 -v=/tmp/known/var/run/secrets:/var/run/secrets -p 8080:8080 maven-build:jdk8 mvn -Dmaven.repo.local=/m2 -f /build spring-boot:run
>
> ```

For more details about the `mount` command check the [documentation](/howto/volumes.html)

### Python Kubernetes Client

If you are using a Python Kubernetes client like [the officially supported one](https://github.com/kubernetes-client/python/), you need to make sure the client can access service account information. This can be done with the `--mount` command introduced in `Telepresence 0.85`.

We need to add the following to the command:

* `--mount /tmp/known` Tells `Telepresence` to mount `TELEPRESENCE_ROOT` to a known folder
* `-v=/tmp/known/var/run/secrets:/var/run/secrets` This is another Docker mounting command to mount the known folder to `/var/run/secrets` in the local container. The [Kubernetes Python client](https://github.com/kubernetes-client/python) can find the secrets there as it would inside Kubernetes.

> telepresence.sh
> ```bash
> telepresence --mount /tmp/known --swap-deployment myapp --docker-run --rm -v$(pwd):/code -v=/tmp/known/var/run/secrets:/var/run/secrets -p 8080:8080 guray/podstatus:1.0
>
> ```

The example is an API which returns list of pods in the desired namespace(*if serviceaccount is authorized*), to try it from your laptop: `curl localhost:8080/pods/default`.

#### How it works?

The container is running on your laptop and gets serviceaccount information like it is on the Kubernetes cluster. Afterwards if authorized, get list of the pods and returns with their status as JSON.

For more details about the `mount` command check the [documentation](/howto/volumes.html)
