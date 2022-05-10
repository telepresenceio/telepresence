---
description: "Create your complete Kubernetes development environment and use Telepresence to intercept services running in your Kubernetes cluster, speeding up local development and debugging."
---

# Creating a local Kubernetes development environment

This tutorial shows you how to use Ambassador Cloud to create an effective Kubernetes development environment to enable  fast, local development with the ability to interact with services and dependencies that run in a remote Kubernetes cluster.

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

## Deploy your application to a remote Kubernetes cluster

First, ensure that your entire application is running in a Kubernetes cluster and available for access to either your users or to yourself acting as a user.

Use your existing `kubectl apply`, `helm install`, or continuous deployment system to deploy your entire application to the remote cluster:

1. Ensure that you have set the correct KUBECONFIG in your local command line/shell in order to ensure your local tooling is interacting with the correct Kubernetes cluster. Verify this by executing `kubectl cluster-info` or `kubectl get svc`.
2. Deploy your application (using kubectl, helm or your CD system), and verify that the services are running with `kubectl get svc`.
3. Verify that you can access the application running by visiting the Ingress IP or domain name. We’ll refer to this as ${INGRESS_IP} from now on.

## Create a local development container to modify a service

After you finish your deployment, you need to configure a copy of a single service and run it locally. This example shows you how to do this in a development container with a sam[;e repository. Unlike a production container, a development container contains the full development toolchain and dependencies required to build and run your application.


1. Clone your code in your repository with `git clone <your-source-code>`.
 For example: `git clone https://github.com/danielbryantuk/gs-spring-boot.git`.
2. Change your directory to the source directory with `cd <your-directory>`.
 To follow the previous example, enter: `cd gs-spring-boot/complete`
3. Ensure that your development environment is configured to support the automatic reloading of the service when your source code changes.
 In the example Spring Boot app this is as simple as [adding the spring-boot-devtools dependency to the pom.xml file](https://docs.spring.io/spring-boot/docs/1.5.16.RELEASE/reference/html/using-boot-devtools.html).
4. Add a Dockerfile for your development.
 To distinguish this from your production Dockerfile, give the development Dockerfile a separate name, like “Dev.Dockerfile”.
 The following is an example for Java:
	```Java
	FROM openjdk:16-alpine3.13

	WORKDIR /app

	COPY .mvn/ .mvn
	COPY mvnw pom.xml ./
	RUN ./mvnw dependency:go-offline

	COPY src ./src

	CMD ["./mvnw", "spring-boot:run"]
	```
5. Next, test that the container is working properly. In the root directory of your source rep, enter:
`docker build -t example-dev-container:0.1 -f Dev.Dockerfile .`
6. Run the development container and mount the current directory as a volume. This way, any code changes you make locally are synchronized into the container. Enter:
 `docker run -v $(pwd):/app example-dev-container:0.1`
 Now, code changes you make locally trigger a reload of the application in the container.
7. Open the current directory with your source code in your IDE. Make a change to the source code and trigger a build/compilation.
 The container logs show that the application has been reloaded.

## Connect your local development environment to the remote cluster

Once you have the development container running, you can integrate your local development environment and the remote cluster. This enables you to access your remote app and instantly see any local changes you have made using your development container.

1. First, download the latest [Telepresence binary](../../install) for your operating system and run `telepresence connect`.
 Your local service is now able to interact with services and dependencies in your remote cluster.
 For example, you can run `curl remote-service-name.namespace:port/path` and get an instant response locally in the same way you would in a remote cluster.
2. Run `telepresence intercept your-service-name` to reroute traffic for the service you’re working on.
3. Make a small change in your local code that will cause a visible change that you will be able to see when accessing your app. Build your service to trigger a reload within the container.
4. Now visit your ${INGRESS_IP} and view the change.
 Notice the instant feedback of a local change combined with being able to access the remote dependencies!
5. Make another small change in your local code and build the application again.
Refresh your view of the app at ${INGRESS_IP}.
 Notice that you didn’t need to re-deploy the container in the remote cluster to view your changes. Any request you make against the remote application that accesses your service will be routed to your local machine allowing you to instantly see the effects of changes you make to the code.
6. Now, put all these commands in a simple shell script, setup-dev-env.sh, which can auto-install Telepresence and configure your local development environment in one command. You can commit this script into your application’s source code repository and your colleagues can easily take advantage of this fast development loop you have created. An example script is included below:
	```
	# deploy your services to the remote cluster
	echo `Add config to deploy the application to your remote cluster via kubectl or helm etc`

	# clone the service you want to work on
	git clone https://github.com/spring-guides/gs-spring-boot.git
	cd gs-spring-boot/complete

	# build local dev container
	docker build -t example-dev-container:0.1 -f Dev.Dockerfile .

	# run local dev container
	# the logs can be viewed by the `docker logs -f <CONTAINER ID>` and the container id can found via `docker container ls`
	docker run -d -v $(pwd):/app example-dev-container:0.1

	# download Telepresence and install (instructions for non Mac users: https://www.getambassador.io/docs/telepresence/v2.4/install/)
	sudo curl -fL https://app.getambassador.io/download/tel2/darwin/amd64/2.4.11/telepresence -o /usr/local/bin/telepresence
	sudo chmod a+x /usr/local/bin/telepresence

	# connect your local dev env to the remote cluster
	telepresence connect

	# re-route remote traffic to your local service
	# telepresence intercept your-service-name

	# happy coding!

	```
## Share the result of your local changes with others

Once you have your local development environment configured for fast feedback, you can securely share access and the ability to view the changes made in your local service with your teammates and stakeholders.

1. Leave any current Telepresence intercepts you have running:
 `telepresence leave your-service-name`
2. Login to Ambassador Cloud using your GitHub account that is affiliated with your organization. This is important because in order to secure control access to your local changes only people with a GitHub account that shares the same organization will be able to view this.
 Run `telepresence login`.
3. Run `telepresence intercept your-service-name` again to reroute traffic for the service you’re working on. This time you will be required to answer several questions about your ingress configuration.
4. Once the command completes, take the “previewURL” that was generated as part of the output and share this with your teammates. Ask them to access the application via this URL (rather than the regular application URL).
5. Make a small change in your local code that causes a visible change that you can see when accessing your app. Build your service to trigger a reload within the container.
6. Ask your teammates to refresh their view of the application and instantly see the local changes you’ve made.

## <img class="os-logo" src="../../images/logo.png"/> What's Next?

Now that you've created a complete Kubernetes development environment, learn more about how to [manage your environment in Ambassador Cloud](https://www.getambassador.io/docs/cloud/latest/service-catalog/howtos/cells) or how to [create Preview URLs in Telepresence](https://www.getambassador.io/docs/telepresence/v2.4/howtos/preview-urls/).
