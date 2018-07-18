# Debugging a Java Rate Limiter Service using Telepresence and IntelliJ IDEA 

## Debugging Locally Against a Remote Kubernetes Cluster using Telepresence 

The fundamental benefit of Telepresence is that it provides a [two-way proxy](https://www.telepresence.io/discussion/why-telepresence#a-fast-development-cycle-with-telepresence) between your local machine and the remote cluster. This means that you can run a service locally (and all of your local debug tooling) and have this service interact with all the other services in the remote cluster. This allows you to make a request against a service running (and exposed) in the remote cluster and proxy a call to a downstream dependent service to your local machine. You can then inspect and modify the request before providing the response from your local machine back into the calling remote service.

### A Brief Video Guide 

The video below provides more information, and also demonstrates how to debug a locally run Rate Limiter service (that is integrated with a remotely deployed Ambassador API Gateway) via Telepresence and IntelliJ IDEA. All of the example code can be found in my [Docker Java Shopping](https://github.com/danielbryantuk/oreilly-docker-java-shopping) sample microservices-based application on GitHub. 

video

### Video Instruction TL;DR

Once you have provisioned a remote Kubernetes cluster you will need to provide your user account with the “cluster-admin” RBAC role and then deploy all of the services configurations within the [kubernetes-ambassador-telepresence](https://github.com/danielbryantuk/oreilly-docker-java-shopping/tree/master/kubernetes-ambassador-telepresence) directory. I’ve included my example kubectl commands below (which are executed against a remote Kubernetes cluster running via [Google’s GKE](https://cloud.google.com/kubernetes-engine/)):

```
$ # Assume Kubernetes cluster has been successfully provisioned
$ #
$ kubectl create clusterrolebinding my-cluster-admin-binding --clusterrole=cluster-admin --user=$(gcloud info --format="value(config.account)")
$ #
$ git clone git@github.com:danielbryantuk/oreilly-docker-java-shopping.git
$ cd oreilly-docker-java-shopping/kubernetes-ambassador-telepresence
$ kubectl apply -f .
```

When all of the services have been deployed successfully, you can use Telepresence to “swap” the remotely running `ratelimiter` deployment with a proxy that will forward all network communications to/from the service that you will run locally:

```
$ telepresence --swap-deployment ratelimiter --env-json ratelimit_env.json
```

You’ll notice that I have specified the `env-json` argument with a filename, which generates a `ratelimit_env.json` file that contains all the relevant Kubernetes cluster environment variables you will need for local debugging. 

## Configuring IntelliJ with Telepresence 

In order to load the generated Env File into IntelliJ, you will need to install the [Env File plugin](https://plugins.jetbrains.com/plugin/7861-env-file). This can be downloaded and installed via the JetBrains website, or you can also install it via the “Preferences -> Plugins” configuration of the IDE itself.

With the plugin installed, you can now clone the [Ambassador Java Rate Limiter](https://github.com/danielbryantuk/ambassador-java-rate-limiter) Java code from GitHub and open this in IntelliJ. The video shows exactly how to configure IntelliJ IDEA, but the primary task is to modify the Run/Debug Configuration to load the Env File that was generated during the Telepresence `swap-deployment` command:

![intelliJ-tutorial](https://www.datawire.io/wp-content/uploads/2018/07/intelliJ-tutorial.png)

With the configuration updated, you can now start the local instance of the `RateLimiter` service in debug mode, and make a request against the remote Kubernetes cluster Shopfront endpoint. Once the request is made then the first breakpoint you have set should be triggered! From here you can debug the locally running service as if it was running within the remote Kubernetes cluster.

## Looking for More Info on Rate Limiting with Ambassador and Kubernetes? 

Check out our [series on rate limiting](https://blog.getambassador.io/tagged/rate-limit-series) with the [Ambassador API Gateway](https://www.getambassador.io/). 
