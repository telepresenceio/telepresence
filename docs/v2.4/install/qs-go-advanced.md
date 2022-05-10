---
description: "Create your complete Kubernetes development environment and use Telepresence to intercept services running in your Kubernetes cluster, speeding up local development and debugging."
---

import Alert from '@material-ui/lab/Alert';

# Creating a local Go Kubernetes development environment

This tutorial shows you how to use Ambassador Cloud to create an effective Kubernetes development environment to enable fast, local development with the ability to interact with services and dependencies that run in a remote Kubernetes cluster.

For the hands-on part of this guide, you will build upon [this tutorial with the emojivoto application](../../quick-start/go/), which is written in Go.

## Prerequisites

To begin, you need a set of services that you can deploy to a Kubernetes cluster. These services must be:

* [Containerized](https://www.getambassador.io/learn/kubernetes-glossary/container/).
 	- Best practices for [writing Dockerfiles](https://docs.docker.com/develop/develop-images/dockerfile_best-practices/).
	- Many modern code editors, such as [VS Code](https://code.visualstudio.com/docs/containers/overview) and [IntelliJ IDEA](https://code.visualstudio.com/docs/containers/overview), can automatically generate Dockerfiles.
* Have a Kubernetes manifest that can be used to successfully deploy your application to a Kubernetes cluster. This includes YAML config files, or Helm charts, or whatever method you prefer.
	- Many modern code editors, such as VS Code, have [plugins](https://marketplace.visualstudio.com/items?itemName=ms-kubernetes-tools.vscode-kubernetes-tools) that will [automatically generate](https://marketplace.visualstudio.com/items?itemName=GoogleCloudTools.cloudcode) a large amount of the Service and Deployment configuration files.
	- The kubectl command-line tool includes a number of [config generators](https://kubernetes.io/docs/reference/kubectl/conventions/#generators) for creating basic Service and Deployment files.
	- For helm users, the [`helm create` command](https://helm.sh/docs/helm/helm_create/) can be used to create the directory and file scaffolding for your chart.
* Follow cloud native application architecture best practices.
	- Design services using the [Twelve-Factor Application](https://12factor.net/) approach.
	- Ensure that your services and ingress gateway include HTTP [header propagation](https://www.getambassador.io/learn/kubernetes-glossary/header-propagation/) for good observability and diagnostics. Many modern language-specific web frameworks support this out-of-the-box, and the [OpenTelemetry documentation](https://opentelemetry.lightstep.com/core-concepts/context-propagation/) also contains good guidance.

The emojivoto example you are exploring in the steps below follows all of these prerequisites.

## Deploy your application to a remote Kubernetes cluster

First, ensure that your entire application is running in a Kubernetes cluster and available for access to either your users or to yourself acting as a user.

Use your existing `kubectl apply`, `helm install`, or continuous deployment system to deploy your entire application to the remote cluster:

1. Ensure that you have set the correct KUBECONFIG in your local command line/shell in order to ensure your local tooling is interacting with the correct Kubernetes cluster. Verify this by executing `kubectl cluster-info` or `kubectl get svc`.
2. Deploy your application (using kubectl, helm or your CD system), and verify that the services are running with `kubectl get svc`.
3. Verify that you can access the application running by visiting the Ingress IP or domain name. We’ll refer to this as ${INGRESS_IP} from now on.

If you followed the [emojivoto application tutorial](../../quick-start/go/) referenced at the beginning of this guide, you will see that your Kubernetes cluster has all of the necessary services deployed and has the ingress configured to expose your application by way of an IP address.

## Create a local development container to modify a service

After you finish your deployment, you need to configure a copy of a single service and run it locally. This example shows you how to do this in a development container with a sample repository. Unlike a production container, a development container contains the full development toolchain and dependencies required to build and run your application.


1. Clone your code in your repository with `git clone <your-source-code>`.
 For example: `git clone https://github.com/danielbryantuk/emojivoto.git`.
2. Change your directory to the source directory with `cd <your-directory>`.
 To follow the previous example, enter: `cd emojivoto-voting-svc/api`
3. Ensure that your development environment is configured to support the automatic reloading of the service when your source code changes.
 In the example, the Go applicationapplication source code is being monitored for changes, and the application is rebuilt with [Air's live-reloading utility](https://github.com/cosmtrek/air).
4. Add a Dockerfile for your development.
 Alternatively, you can use a Cloud Native Buildpack, such as those provided by Google Cloud. The [Google Go buildpack](https://github.com/GoogleCloudPlatform/buildpacks) has live-reloading configured by default.
5. Next, test that the container is working properly. In the root directory of your source rep, enter:
`docker build -t example-dev-container:0.1 -f Dev.Dockerfile .`
If you ran the the [emojivoto application example](../../quick-start/go/), the container has already been built for you and you can skip this step.
6. Run the development container and mount the current directory as a volume. This way, any code changes you make locally are synchronized into the container. Enter:
 `docker run -v $(pwd):/opt/emojivoto/emojivoto-voting-svc/api datawire/telepresence-emojivoto-go-demo`
 Now, code changes you make locally trigger a reload of the application in the container.
7. Open the current directory with your source code in your IDE. Make a change to the source code and trigger a build/compilation.
 The container logs show that the application has been reloaded.

If you followed the [emojivoto application tutorial](../../quick-start/go/) referenced at the beginning of this guide, the emojivoto development container is already downloaded. When you examine the `docker run` command you executed, you can see an AMBASSADOR_API_KEY token included as an environment variable. Copy and paste this into the example command below. Clone the emojivoto code repo and run the container with the updated configuration to expose the application's ports locally and volume mount your local copy of the application source code into the container:
```
$ git clone git@github.com:danielbryantuk/emojivoto.git
$ cd emojivoto-voting-svc/api
$ docker run -d -p8083:8083 -p8081:8081 --name voting-demo --cap-add=NET_ADMIN --device /dev/net/tun:/dev/net/tun --pull always --rm -it -e AMBASSADOR_API_KEY=<Add your key here>  -v ~/Library/Application\ Support:/root/.host_config -v $(pwd):/opt/emojivoto/emojivoto-voting-svc/api datawire/telepresence-emojivoto-go-demo
```

## Connect your local development environment to the remote cluster

Once you have the development container running, you can integrate your local development environment and the remote cluster. This enables you to access your remote app and instantly see any local changes you have made using your development container.

1. First, download the latest [Telepresence binary](../../install/) for your operating system and run `telepresence connect`.
 Your local service is now able to interact with services and dependencies in your remote cluster.
 For example, you can run `curl remote-service-name.namespace:port/path` and get an instant response locally in the same way you would in a remote cluster.
2. Extract the KUBECONFIG from your dev container from the [emojivoto application tutorial](../../quick-start/go/) and then connect your container to the remote cluster with Telepresence:
	```
	$ CONTAINER_ID=$(docker inspect --format="{{.Id}}" "/voting-demo")
	$ docker cp $CONTAINER_ID:/opt/telepresence-demo-cluster.yaml ./emojivoto_k8s_context.yaml
	```
3. Run `telepresence intercept your-service-name` to reroute traffic for the service you’re working on:
	```
	$ telepresence intercept voting --port 8081:8080
	```
4. Make a small change in your local code that will cause a visible change that you will be able to see when accessing your app. Build your service to trigger a reload within the container.
5. Now visit your ${INGRESS_IP} and view the change.
 Notice the instant feedback of a local change combined with being able to access the remote dependencies!
6. Make another small change in your local code and build the application again.
Refresh your view of the app at ${INGRESS_IP}.
 Notice that you didn’t need to re-deploy the container in the remote cluster to view your changes. Any request you make against the remote application that accesses your service will be routed to your local machine allowing you to instantly see the effects of changes you make to the code.
7. Now, put all these commands in a simple shell script, setup-dev-env.sh, which can auto-install Telepresence and configure your local development environment in one command. You can commit this script into your application’s source code repository and your colleagues can easily take advantage of this fast development loop you have created. An example script is included below, which follows the “[Do-nothing scripting](https://blog.danslimmon.com/2019/07/15/do-nothing-scripting-the-key-to-gradual-automation/)"" format from Dan Slimmon:

	```
	#!/bin/bash

	# global vars
	CONTAINER_ID=''

	check_init_config() {
	    if [[ -z "${AMBASSADOR_API_KEY}" ]]; then
	        # you will need to set the AMBASSADOR_API_KEY via the command line
	        # export AMBASSADOR_API_KEY='NTIyOWExZDktYTc5...'
	        echo 'AMBASSADOR_API_KEY is not currently defined. Please set the environment variable in the shell e.g.'
	        echo 'export AMBASSADOR_API_KEY=NTIyOWExZDktYTc5...'
	        exit
	    fi
	}

	run_dev_container() {
	    echo 'Running dev container (and downloading if necessary)'

	    # check if dev container is already running and kill if so
	    CONTAINER_ID=$(docker inspect --format="{{.Id}}" "/voting-demo" > /dev/null 2>&1 )
	    if [ ! -z "$CONTAINER_ID" ]; then
	        docker kill $CONTAINER_ID
	    fi

	    # run the dev container, exposing 8081 gRPC port and volume mounting code directory
	    CONTAINER_ID=$(docker run -d -p8083:8083 -p8081:8081 --name voting-demo --cap-add=NET_ADMIN --device /dev/net/tun:/dev/net/tun --pull always --rm -it -e AMBASSADOR_API_KEY=$AMBASSADOR_API_KEY  -v ~/Library/Application\ Support:/root/.host_config -v $(pwd):/opt/emojivoto/emojivoto-voting-svc/api datawire/telepresence-emojivoto-go-demo)
	}

	connect_to_k8s() {
	    echo 'Extracting KUBECONFIG from container and connecting to cluster'
	    until docker cp $CONTAINER_ID:/opt/telepresence-demo-cluster.yaml ./emojivoto_k8s_context.yaml > /dev/null 2>&1; do
	        echo '.'
	        sleep 1s
	    done

	    export KUBECONFIG=./emojivoto_k8s_context.yaml

	    echo 'Connected to cluster. Listing services in default namespace'
	    kubectl get svc
	}

	install_telepresence() {
	    echo 'Configuring Telepresence'
	    if [ ! command -v telepresence &> /dev/null ];  then
	        echo "Installing Telepresence"
	        sudo curl -fL https://app.getambassador.io/download/tel2/darwin/amd64/2.4.11/telepresence -o /usr/local/bin/telepresence
	        sudo chmod a+x /usr/local/bin/telepresence
	    else
	        echo "Telepresence already installed"
	    fi
	}

	connect_local_dev_env_to_remote() {
	    export KUBECONFIG=./emojivoto_k8s_context.yaml
	    echo 'Connecting local dev env to remote K8s cluster'
	    telepresence intercept voting --port 8081:8080
	}

	open_editor() {
	    echo 'Opening editor'

	    # replace this line with your editor of choice, e.g. VS code, Intelli J
	    code .
	}

	display_instructions_to_user () {
	    echo ''
	    echo 'INSTRUCTIONS FOR DEVELOPMENT'
	    echo '============================'
	    echo 'To set the correct Kubernetes context on this shell, please execute:'
	    echo 'export KUBECONFIG=./emojivoto_k8s_context.yaml'
	}

	check_init_config
	run_dev_container
	connect_to_k8s
	install_telepresence
	connect_local_dev_env_to_remote
	open_editor
	display_instructions_to_user

	# happy coding!

	```
8. Run the setup-dev-env.sh script locally. Use the $AMBASSADOR_API_KEY you created from Docker in the [emojivoto application tutorial](../../quick-start/go/) or in [Ambassador Cloud](https://app.getambassador.io/cloud/services/).
	```
	export AMBASSADOR_API_KEY=<your key>
	git clone git@github.com:danielbryantuk/emojivoto.git
	cd emojivoto-voting-svc/api
	./setup_dev_env.sh
	```
	<Alert severity="info">
	If you are not using Mac OS and not using VS Code, you will need to update the script to download the correct Telepresence binary for your OS and open the correct editor, respectively
	</Alert>

## Share the result of your local changes with others

Once you have your local development environment configured for fast feedback, you can securely share access and the ability to view the changes made in your local service with your teammates and stakeholders.

1. Leave any current Telepresence intercepts you have running:
 `telepresence leave your-service-name`
2. Login to Ambassador Cloud using your GitHub account that is affiliated with your organization. This is important because in order to secure control access to your local changes only people with a GitHub account that shares the same organization will be able to view this.
 Run `telepresence login`.
3. Run `telepresence intercept your-service-name` again to reroute traffic for the service you’re working on. This time you will be required to answer several questions about your ingress configuration.
4. Once the command completes, take the “previewURL” that was generated as part of the output and share this with your teammates. Ask them to access the application via this URL (rather than the regular application URL).
5. Make a small change in your local code that causes a visible change that you can see when accessing your app. Build your service to trigger a reload within the container.
6. Run the following three commands to generate a link to share with your teammates:
	```
	$ telepresence leave voting
	$ telepresence login
	$ telepresence intercept voting --port 8081:8080
	```
7. Ask your teammates to refresh their view of the application and instantly see the local changes you’ve made.

## <img class="os-logo" src="../../images/logo.png"/> What's Next?

Learn more about creating intercepts in your Telepresence environment with the [Intercept a service in your own environment](../../howtos/intercepts/) documentation.
