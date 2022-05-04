# Create an Intercept

## Prerequisites

Before you begin, you need to have [Docker Desktop](https://www.docker.com/products/docker-desktop) and the Telepresence for Docker extension [installed](../install), as well as the Kubernetes command-line tool, [kubectl](https://kubernetes.io/docs/tasks/tools/install-kubectl/).

This guide assumes you have a Kubernetes deployment with a running service, and that you can run a copy of that service in a docker container on your laptop.

## Intercept your service with a personal intercept

With the Telepresence for Docker extension, you can create [personal intercepts](../../concepts/intercepts/?intercept=personal) that intercept your cluster traffic that passes through a provided proxy url and routes it to your local docker container instead.

1. Create a Copy of the Service You Want to Intercept

   Telepresence for Docker routes traffic to and from a local docker container. We need to setup a docker container to recieve this traffic. To do this, run

   ```console
   $ docker run --rm -it --network host <your image>
   ```

   Telepresence for Docker requires the target service to be on the host network. This allows Telepresence to share a network with your container. The mounted network device redirects cluster-related traffic back into the cluster.

2. Login to Ambassador Cloud

   To connect Telepresence for Docker to your account, you will need an API key.

   1. Click the "Generate API Key" button to open a browser

   2. Login using Google, Github, or Gitlab

   3. You will be taken to your API Key page. Copy and paste it into the API form in the Docker Dashboard, and press Login.

3. Connect to your cluster

   1. Use the dropdown to choose the cluster you would like to use. The chosen cluster will be set to kubectl's current context. Press next.

   2. Press "Connect to (your cluster)" to establish a connection.

4. Intercept a service

   Once your are connected, Telepresence for Docker will discover services in the default namesapce and list them in a table. These are the services you can intercept in this namespace. To switch namespaces, choose a different namespace from the dropdown.

   1. Choose a service to intercept and click the "Intercept" button for that service. A popup will appear with port options.

   2. Then choose the target port, this is the port of the service in the docker conatiner we setup in step one.

   3. Choose the service port of the service you would like to intercept from the dropdown.

   4. Press Submit, a intercept will be created.

5. Query the environment in which you intercepted a service and verify your local instance being invoked.

   All the traffic previously routed to and from your Kubernetes Service is now routed to and from your local container. Click the share button next to your Intercept to open your intercept in a browser, or to view your intercept in Ambassador Cloud.
