---
description: "Claim a remote demo cluster and learn to use Telepresence to intercept services running in a Kubernetes Cluster, speeding up local development and debugging."
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
import Platform from '@src/components/Platform';
import QSCards from './qs-cards';
import { UserInterceptCommand, DemoClusterWarning } from '../../../../../src/components/Docs/Telepresence';

# Telepresence Quick Start

<div class="docs-article-toc">
<h3>Contents</h3>

* [1. Get a free remote cluster](#1-get-a-free-remote-cluster)
* [2. Try the Emojivoto application](#2-try-the-emojivoto-application)
* [3. Set up your local development environment](#3-set-up-your-local-development-environment)
* [4. Testing our fix](#4-testing-our-fix)
* [5. Preview URLs](#5-preview-urls)
* [6. How/Why does this all work](#6-howwhy-does-this-all-work)
* [What's next?](#img-classos-logo-srcimageslogopng-whats-next)

</div>

In this guide, we'll give you a hands-on tutorial with Telepresence. To go through this tutorial, the only thing you'll need is a computer that runs Docker Desktop >=20.10.7. We'll give you a pre-configured remote Kubernetes cluster and a Docker container to run locally.

If you don't have Docker Desktop already installed, go to the [Docker download page](https://www.docker.com/get-started) and install Docker.

<Alert severity="info">
    While Telepresence works with any language, this guide uses a sample app written in Node.js and Golang. We have a version in <a href="../demo-react/">React</a> if you prefer.
</Alert>

## 1. Get a free remote cluster

Telepresence connects your local workstation with a remote Kubernetes cluster. In this tutorial, we'll start with a pre-configured, remote cluster.

1. <Login urlParams="docs_source=telepresence-quick-start&login_variant=free-cluster-activation" origin="telepresence-novice-quick-start" />
2. Go to the <DCPLink>Service Catalog</DCPLink> to see all the services deployed on your cluster.
   <EmojivotoServicesList/>
    The Service Catalog gives you a consolidated view of all your services across development, staging, and production. After exploring the Service Catalog, continue with this tutorial to test the application in your demo cluster.

<DemoClusterWarning />

<div className="docs-opaque-section">

## 2. Try the Emojivoto application

The remote cluster is running the Emojivoto application, which consists of four services. Test out the application:

1. Go to the <ExternalIp/> and vote for some emojis.
    <Alert severity="info">
    If the link to the remote demo cluster doesn't work, make sure you don't have an <strong>ad blocker</strong> preventing it from opening.
    </Alert>

2. Now, click on the 🍩 emoji. You'll see that a bug is present, and voting 🍩 doesn't work. We're going to use Telepresence shortly to fix this bug, as everyone should be able to vote for 🍩!

<Alert severity="success">
    <strong>Congratulations!</strong> You've successfully accessed the Emojivoto application on your remote cluster.
</Alert>

## 3. Set up your local development environment

We'll set up a development environment locally on your workstation. We'll then use Telepresence to connect this local development environment to the remote Kubernetes cluster. To save time, the development environment we'll use is pre-packaged as a Docker container.

1. Run the Docker container locally, by running this command inside your local terminal:

<Platform.TabGroup>
<Platform.MacOSTab>

<DockerCommand osType="macos"/>

</Platform.MacOSTab>
<Platform.GNULinuxTab>

<DockerCommand osType="linux"/>

</Platform.GNULinuxTab>
<Platform.WindowsTab>

<DockerCommand osType="windows"/>

</Platform.WindowsTab>
</Platform.TabGroup>


<Alert severity="info">
Make sure that ports <strong>8080</strong> and <strong>8083</strong> are free. <br/>
If the Docker engine is not running, the command will fail and you will see <strong>docker: unknown server OS</strong> in your terminal.
</Alert>

2. The Docker container includes a copy of the Emojivoto application that fixes the bug. Visit the [leaderboard](http://localhost:8083/leaderboard) and notice how it is different from the leaderboard in your <ExternalIp>Kubernetes cluster</ExternalIp>.

3. Vote for 🍩 on your local leaderboard, and you can see that the bug is fixed!

<Alert severity="success">
  <strong>Congratulations!</strong> You have successfully set up a local development environment, and tested the fix locally.
</Alert>

## 4. Testing our fix

A common use case for Telepresence is to connect your local development environment to a remote cluster. This way, if your application is too big or complex to run locally, you can still develop locally. In this Quick Start, we're also going to show Telepresence can be used for integration testing, by testing our fix against the services in the remote cluster.

1. From your Docker container, create an intercept, which will tell Telepresence to send traffic to the service in your container instead of the service in the cluster:
   `telepresence intercept web --port 8080`

    When prompted for ingress configuration, all default values should be correct as displayed below.

    <UserInterceptCommand/>

<Alert severity="success">
    <strong>Congratulations!</strong> Traffic to the remote service is now being routed to your local laptop, and you can see how the local fix works on the remote environment!
</Alert>

## 5. Preview URLs

Preview URLs enable you to safely share your development environment with anyone. For example, you may want your UX designer to take a quick look at what you're developing, before you commit the code. Preview URLs enable this easy collaboration.

1. If you access the Emojivoto application on <ExternalIp> your remote cluster </ExternalIp> and vote for the 🍩 emoji, you'll see the bug is still present.

2. Vote for the 🍩 emoji using the <PreviewUrl>Preview URL</PreviewUrl> obtained in the previous step, and you will see that the bug is fixed, since traffic is being routed to the fixed version running locally.

<Alert severity="success">
Now you're able to share your fix in your local environment with your team!
</Alert>

<Alert severity="info">
    To get more information regarding Preview URLs and intercepts, visit the <DCPLink>Developer Control Plane </DCPLink>.
</Alert>

</div>

## 6. How/Why does this all work?

Telepresence works by deploying a two-way network proxy in a pod running in a Kubernetes cluster. This proxy can intercept traffic meant for the service and reroute it to a local copy, which is ready for further (local) development.

Intercepts and preview URLs are functions of Telepresence that enable easy local development from a remote Kubernetes cluster and offer a preview environment for sharing and real-time collaboration.

Telepresence also uses custom headers and header propagation for controllable intercepts and preview URLs. The headers facilitate the smart routing of requests either to live services in the cluster or services running locally on a developer’s machine.

Preview URLs, when created, generate an ingress request containing a custom header with a token (the context). Telepresence sends this token to Ambassador Cloud with other information about the preview. Visiting the preview URL directs the user to Ambassador Cloud, which proxies the user to the cluster ingress with the token header injected into the request. The request carrying the header is routed in the cluster to the appropriate pod (the propagation). The Traffic Agent on the service pod sees the header and intercepts the request, redirecting it to the local developer machine that ran the intercept.

## <img class="os-logo" src="../../images/logo.png"/> What's Next?


You've intercepted a service in one of our demo clusters, now you can use Telepresence to [intercept a service in your own environment](https://www.getambassador.io/docs/telepresence/latest/howtos/intercepts/)!
