# Local development with Java
*Author: Cesar Tron-Lozai ([@CesarTronLozai](https://twitter.com/cesarTronLozai))*

{% import "../macros.html" as macros %}
{{ macros.install("https://kubernetes.io/docs/tasks/tools/install-kubectl/", "kubectl", "Kubernetes", "top") }}

### Java

`Telepresence` can help you speed up your development process for any technology, as long as you deploy your service as a Docker image into a Kubernetes container.

In this tutorial we will focus on how to setup a local development environment for a (micro)-service `Foo` written in Java.

This is is very useful if your application is formed of many such services which cannot run on a single development machine. In which case it's easy to setup a separate Kubernetes cluster dedicated for development.

`Telepresence` will help us locally develop our service `Foo` as if it was still inside the Kubernetes cluster. It's a win-win!!

### Architecture

The idea is quite simple, `Telepresence` will start a Docker container on your local machine, remove the running pod for `Foo` and replace it with a two-way proxy to your local docker container.

If other services in your cluster want to talk to `Foo`, they'll get redirected to your local process. If your local process wants to talk to any other services running in your cluster, `Telepresence` will redirect the calls to your cluster.
It will also maintain all the environment variables defined in your deployment. It's magical.

In order to run our Java application in a local Docker container, we can simply start a container which has Java and Maven/Gradle installed, mount the source directory to our code, and do a Maven/Gradle build.

In this tutorial we will be using Maven and a Spring Boot project but this would work exactly the same with Gradle or any other Java Framework.

### Building inside a docker container

As mentioned above, the goal is to compile and run our code inside a Docker container which `Telepresence` can use to replace the pod running in your cluster.

Let's build the command step by step.

* `telepresence` Runs `Telepresence`!
* `--swap-deployment foo` Assumes we already have a `foo` deployment running in our clusters. For different options check the documentation!
* `--docker-run` Tells `Telepresence` to run a Docker containers
* `--rm` Tells Docker to discard our image when it terminates (no need to clutter your computer)
* `-v$(pwd):/build` Mounts the current directory (result of the `pwd` command) into a `/build` folder inside the Docker container. This is where your source code will be; in this case used by Maven.
* `-v $HOME/.m2/repository:/m2` Mounts the Maven cache folder so we don't have to download Maven artifacts every time we run the container
* `-p 8080:8080` That's optional. If your container is running a server on, for example, port `8080`, you can map that port if you need to make requests to your service directly
* `maven:3.5.3-jdk-8-slim` That's the image which we will use to build and run our service. Here this is a prebuilt image containing Maven and Java 8. Use any other image to match your need
* `mvn -Dmaven.repo.local=/m2 -f /build spring-boot:run` Command to be run in the Docker container. Here it uses the Spring Boot Maven plugin but you can use whatever command required by your build tool. It tells maven to point to the mounted repository cache and where the source code located

And that's it! You can easily create a `telepresence.sh` file in the root of your project with the following:

> telepresence.sh
> ```bash
> telepresence --swap-deployment foo --docker-run --rm -v$(pwd):/build -v $HOME/.m2/repository:/m2 -p 8080:8080 maven-build:jdk8 mvn -Dmaven.repo.local=/m2 -f /build spring-boot:run
>
> ```

### Kubernetes Client

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

### Debugging your code

If you need to debug your code with your favourite IDE that's super easy too. You only need to pass a JVM argument and forward the remote port:

* `-e MAVEN_OPTS=-agentlib:jdwp=transport=dt_socket,server=y,suspend=n,address=*:5005` Creates a Docker environment variable that Maven will use to set a JVM argument and awaits for a remote connection on port `5005`. This is for Java 8+, check the Java documentation if you are running a lower version.
* `-p 5005:5005` Tells docker to forward that ports from your local machine.

Then you can use your IDE to start a debug remote session on your local port `5005`

> telepresence-debug.sh
> ```bash
> telepresence --swap-deployment foo --docker-run --rm -e MAVEN_OPTS=-agentlib:jdwp=transport=dt_socket,server=y,suspend=n,address=*:5005 -v$(pwd):/build -v $HOME/.m2/repository:/m2 -p 8080:8080 -p 5005:5005 maven-build:jdk8 mvn -Dmaven.repo.local=/m2 -f /build spring-boot:run
>
> ```

### Hot code replace

If you have a [JRebel Licence](https://zeroturnaround.com/software/jrebel/) you can also integrate it with Telepresence.

Normally you would need to use JRebel remote when your application is running inside a Docker container. However your docker container shares the source folder so you can use that directly.

First you need to create a `rebel.xml` file that will tell JRebel where the source code is, that is in the `/build` folder

```xml
<?xml version="1.0" encoding="ISO-8859-1"?>
<application xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns="http://www.zeroturnaround.com"
             xsi:schemaLocation="http://www.zeroturnaround.com http://update.zeroturnaround.com/jrebel/rebel-2_1.xsd">
    <classpath>
        <dir name="/build/target/classes">
        </dir>
    </classpath>
</application>
```

You should copy `rebel.xml` in your `src/main/resources` folder

If you haven't downloaded JRebel yet, do so [here](https://zeroturnaround.com/software/jrebel/download/) and unzip it somewhere.

You can create a `JREBEL` environment variable that point to this folder. That means `$JREBEL/jrebel.jar` should be a valid file.

To activate JRebel, you need the following:

* `-v $JREBEL:/jrebel` Mounts the JRebel folder
* `-v $JREBEL/jrebel.jar:/jrebel.jar` Makes `jrebel.jar` available to JREBEL
* `-v $HOME/.jrebel:/root/.jrebel` Mounts your JRebel home folder, this gives access to the licence and JRebel stats. This assumes the home folder of the process in your docker image is `/root`, change if required
* `-Drun.jvmArguments="-agentpath:/jrebel/lib/libjrebel64.so"` Tells the JVM to use the Linux64 JRebel agent

> telepresence-jrebel.sh
> ```bash
> telepresence --swap-deployment foo --docker-run --rm -v $JREBEL:/jrebel -v $JREBEL/jrebel.jar:/jrebel.jar -v $HOME/.jrebel:/root/.jrebel -v$(pwd):/build -v $HOME/.m2/repository:/m2 -p 8080:8080 maven-build:jdk8 mvn -Drun.jvmArguments="-agentpath:/jrebel/lib/libjrebel64.so" -Dmaven.repo.local=/m2 -f /build spring-boot:run
>
> ```

### Example
If you want to see a simple Spring boot project using telepresence have a look at https://github.com/cesartl/telepresence-k8s
