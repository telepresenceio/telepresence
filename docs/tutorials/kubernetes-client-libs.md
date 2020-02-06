# Local Connection to Kubernetes Client Libraries
*Author: Guray Yildirim ([@gurayyildirim](https://twitter.com/gurayyildirim))*

{% import "../macros.html" as macros %}
{{ macros.install("https://kubernetes.io/docs/tasks/tools/install-kubectl/", "kubectl", "Kubernetes", "top") }}


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

