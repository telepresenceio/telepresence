---
description: "Install Telepresence and learn to use it to intercept services running in your Kubernetes cluster, speeding up local development and debugging."
---

import {DemoClusterMetadata, ExpirationDate} from '../../../../../src/components/DemoClusterMetadata';
import {
EmojivotoServicesList,
DCPLink,
Login,
LoginCommand,
DockerCommand,
PreviewUrl,
ExternalIp
} from '../../../../../src/components/Docs/Telepresence';
import Alert from '@material-ui/lab/Alert';
import QSCards from './qs-cards';
import { UserInterceptCommand, DemoClusterWarning } from '../../../../../src/components/Docs/Telepresence';


<div class="docs-language-toc">

* <a href="../qs-go/" title="Go" class="active">Go</a>
* <a href="../demo-node/" title="Node">Node</a>

</div>

# Telepresence Quick Start - **Go**

In this guide, we'll give you a hands-on tutorial with Telepresence. To go through this tutorial, the only thing you'll need is a computer that runs Docker Desktop >=20.10.7. We'll give you a pre-configured remote Kubernetes cluster and a Docker container to run locally.

If you don't have Docker Desktop already installed, go to the [Docker download page](https://www.docker.com/get-started) and install Docker.

## 1. Get a free remote cluster

Telepresence connects your local workstation with a remote Kubernetes cluster. In this tutorial, we'll start with a pre-configured, remote cluster.

1. <Login urlParams="docs_source=telepresence-quick-start&login_variant=free-cluster-activation"/>
2. Go to the <DCPLink>Service Catalog</DCPLink> to see all the services deployed on your cluster.
   <EmojivotoServicesList/>
    The Service Catalog gives you a consolidated view of all your services across development, staging, and production. After exploring the Service Catalog, continue with this tutorial to test the application in your demo cluster.

<DemoClusterWarning />

<div className="docs-opaque-section">

## 2. Try the Emojivoto application

The remote cluster is running the Emojivoto application, which consists of four services. Test out the application:

1. Go to the <a href="">emoji-web-app</a> and vote for some emojis.
   <Alert severity="info">
   If the link to the remote demo cluster doesn't work, make sure you don't have an <strong>ad blocker</strong> preventing it from opening.
   </Alert>

2. Now, click on the 🍩 emoji. You'll see that a bug is present, and voting 🍩 doesn't work.

## 3. Run the docker container

We'll set up a development environment locally on your workstation. We'll use Telepresence to connect this local environment to the remote Kubernetes cluster. To save time, the development environment we'll use is pre-packaged as a Docker container.

1. Run the Docker container locally: 

  ```
  $ docker run -p 80:80 -p 9090:9090 --name ambassador-demo --pull always --rm -it datawire/demo-go-emojivoto
  	Connected to context telepresence-demo (https://$DEMO_CLUSTER_IP)
	  emoji             : ready to intercept (traffic-agent not yet installed)
	  web               : ready to intercept (traffic-agent not yet installed)
	  voting            : ready to intercept (traffic-agent not yet installed)
	  web-app-778477c59c: ready to intercept (traffic-agent not yet installed)
  ```

  If you reload the <a href="">emoji-web-app</a> you can notice the error still happening but now we are using a *local development environment.*


2. The application is failing due to a little bug inside the Golang `emojivoto-voting-svc`, which uses gRPC to communicate with the others services. We can use `grpc-cli` to test the gRPC endpoint and  see the bug by running:

  ```
  $ grpc-cli vote-donut

    Error reaching out the grpc-emoji-svc
  ```

3. In order to fix the bug, the docker container comes with an embedded IDE that runs in the browser, you can go to <a href="#">http://localhost:9000</a> and open `main.go` at line `50` you'll find:

  ```go
    if (path.url == "/emoji/donut") {
      return new Error("Error while voting for donut"), nil
    }
  ```

  You can delete those (`50`-`58`) lines to fix it, after that you can verify it's fixed running again the cli command:

  ```
  $ grpc-cli vote-donut

    Donut vote done correctly
  ```

## 4. Telepresence intercept

1. Now the bug is fixed, we can go to <a href="#">emoji-web-app</a> and after refreshing the page, we will see that it works as expected.

2. We just created an intercept, this tell Telepresence where to send traffic. In this docker container, we sent all the traffic destined to `emoji-svc` to the local Dockerized version of the service. To do that, we ran  `telepresence intercept emoji-svc --port 3000` inside the docker container, In this way we intercept all the traffic to our local `emoji-svc` service and since we already fixed it, now it works.

## 5. Telepresence intercept with a preview URL

Preview URLs allows you to safely share your development environment. With this approach, you can try and test your local service more accurately because you have a total control about which traffic is handled through your service, all of this thank to the preview URL.  

1. First from the Telepresence CLI which is running in the container, run: 

  <LoginCommand />

2. Create an intercept, which will tell Telepresence to send traffic to the service in our container instead of the service in the cluster:

  `telepresence intercept web --port 8080`

  When prompted for ingress configuration, all default values should be correct as displayed below.

  <UserInterceptCommand/>

3. If you access the <a href="#">emoji-web-app</a> application on your remote cluster and vote for the 🍩 emoji, you'll see the bug is still present.

4. Vote for the 🍩 emoji using the <PreviewUrl>Preview URL</PreviewUrl> obtained in the previous step, and you will see that the bug is fixed, since traffic is being routed to the fixed version running locally.

</div>

## <img class="os-logo" src="../../images/logo.png"/> What's Next?


You've intercepted a service in one of our demo clusters, now you can use Telepresence to [intercept a service in your own environment](https://www.getambassador.io/docs/telepresence/latest/howtos/intercepts/)!
