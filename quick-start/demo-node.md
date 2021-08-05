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
    DemoClusterMetadataError,
    ExternalIp,
    InterceptsLink,
} from '../../../../../src/components/Docs/Telepresence';
import Alert from '@material-ui/lab/Alert';
import QSTabs from './qs-tabs';
import QSCards from './qs-cards';
import { UserInterceptCommand } from '../../../../../src/components/Docs/Telepresence';

# Telepresence Quick Start

<div class="docs-article-toc">
<h3>Contents</h3>

* [1. Get a free remote cluster](#1-get-a-free-remote-cluster)
* [2. Try the Emojivoto application](#2-try-the-emojivoto-application)
* [3. Set up your local development environment](#3-set-up-your-local-development-environment)
* [4. Testing our fix](#4-testing-our-fix)
* [5. Preview URLs](#5-preview-urls)
* [6. Go to see your intercepts](#6-go-to-see-your-intercepts)
* [7. How/Why does this all work](#7-howwhy-does-this-all-work)
* [What's next?](#img-classos-logo-srcimageslogopng-whats-next)

</div>

In this guide, we'll give you a hands-on tutorial with Telepresence. To go through this tutorial, the only thing you'll need is a computer that runs Docker Desktop. We'll give you a pre-configured remote Kubernetes cluster and a Docker container to run locally.

If you don't have Docker Desktop already installed, go to the [Docker download page](https://www.docker.com/get-started) and install Docker.

<Alert severity="info">
    While Telepresence works with any language, this guide uses a sample app written in Node.js and Golang. We have a version in <a href="../demo-react/">React</a> if you prefer.
</Alert>

<Alert severity="info"> 
    Note: This documentation will dynamically update with values once you authenticate to Ambassador Cloud in step one below. If you need help, please join the <strong>#telepresence</strong> <a href="https://a8r.io/Slack">Slack channel</a>.
</Alert>

## 1. Get a free remote cluster

Telepresence connects your local workstation with a remote Kubernetes cluster. In this tutorial, we'll start with a pre-configured, remote cluster.

<Alert severity="info">
    <strong>Already have a cluster?</strong> Switch over to a <a href="../qs-node">version of this guide</a> that takes you though the same steps using your own cluster.
</Alert>

1. <Login/> Note where you've downloaded the <code>kubeconfig.yaml</code> file; you'll need the location of this file later in this guide.

<DemoClusterMetadataError/>

<Alert severity="success">
   The Service Catalog gives you a consolidated view of all your services across development, staging, and production. 
</Alert>

<ExpirationDate/>

## 2. Try the Emojivoto application

The remote cluster is running the Emojivoto application, which consists of three services. Test out the application:

1. Go to the <ExternalIp/> and vote for some emojis.

2. Now, click on the 🍩 emoji. You'll see that a bug is present, and voting 🍩 doesn't work. We're going to use Telepresence shortly to fix this bug, as everyone should be able to vote for 🍩!
   
<Alert severity="success">
    <strong>Congratulations!</strong> You've successfully accessed the Emojivoto application on your remote cluster.
</Alert>

## 3. Set up your local development environment

We'll set up a development environment locally on your workstation. We'll then use Telepresence to connect this local development environment to the remote Kubernetes cluster. To save time, the development environment we'll use is pre-packaged as a Docker container.

1. Run the Docker container locally. In the command below, replace the path to the `kubeconfig.yaml` with the actual location of the `kubeconfig.yaml` you previously noted in [step 1](#1-get-a-free-remote-cluster):

    <DockerCommand/>

2. The Docker container includes a copy of the Emojivoto application that fixes the bug. Visit the [leaderboard](http://localhost:8083/leaderboard) and notice how it is different from the leaderboard in your <ExternalIp>Kubernetes cluster</ExternalIp>.

3. Vote for 🍩 on your local leaderboard, and you can see that the bug is fixed!

<Alert severity="success">
  <strong>Congratulations!</strong> You have successfully set up a local development environment, and tested the fix locally.
</Alert>

## 4. Testing our fix

A common use case for Telepresence is to connect your local development environment to a remote cluster. This way, if your application is too big or complex to run locally, you can still develop locally. In this Quick Start, we're also going to show Telepresence can be used for integration testing, by testing our fix against the services in the remote cluster.

1. First, log in to Telepresence using your API key:
<LoginCommand/>

2. Create an intercept, which will tell Telepresence to send traffic to the service in our container instead of the service in the cluster:
    `telepresence intercept web --port 8080`

   You will be asked for your ingress layer 3 address; specify the front end service: `ambassador.ambassador`
   Then, when asked for the port, type `80`, for "use TLS", type `n`.  The default for the fourth value is correct so hit enter to accept it.
    
    <UserInterceptCommand/>

<Alert severity="success">
    <strong>Congratulations!</strong> Traffic to the remote service is now being routed to your local laptop, and you can see how the local fix works on the remote environment!
</Alert>

## 5. Preview URLs

Preview URLs enable you to safely share your development environment with anyone. For example, you may want your UX designer to take a quick look at what you're developing, before you commit the code. Preview URLs enable this easy collaboration.

2. If you access the Emojivoto application on <ExternalIp> your remote cluster </ExternalIp> and vote for the 🍩 emoji, you'll see the bug is still present.
   
1. Vote for the 🍩 emoji using the <PreviewUrl>Preview URL</PreviewUrl> obtained in the previous step, and you will see that the bug is fixed, since traffic is being routed to the fixed version running locally.
   <Alert severity="success">
        Now you're able to share your fix in your local environment with your team!
   </Alert>
   
   <Alert severity="info">
        To get more information regarding Preview URLs and intercepts, visit the <DCPLink>Developer Control Plane </DCPLink>.
   </Alert>

## 6. Visualize and manage your Preview URLs and intercepts

1. The Developer Control Plane lets you manage & visualize important information about your intercepts. Visit the <InterceptsLink>Developer Control Plane UI</InterceptsLink> to see who's acceced your preview URL.

## 7. How/Why does this all work

Telepresence works by deploying a two-way network proxy in a pod running in a Kubernetes cluster. This proxy can intercept traffic meant for the service and reroute it to a local copy, which is ready for further (local) development.

Intercepts and preview URLs are functions of Telepresence that enable easy local development from a remote Kubernetes cluster and offer a preview environment for sharing and real-time collaboration.

Telepresence also uses custom headers and header propagation for controllable intercepts and preview URLs. The headers facilitate the smart routing of requests either to live services in the cluster or services running locally on a developer’s machine.

Preview URLs, when created, generate an ingress request containing a custom header with a token (the context). Telepresence sends this token to Ambassador Cloud with other information about the preview. Visiting the preview URL directs the user to Ambassador Cloud, which proxies the user to the cluster ingress with the token header injected into the request. The request carrying the header is routed in the cluster to the appropriate pod (the propagation). The Traffic Agent on the service pod sees the header and intercepts the request, redirecting it to the local developer machine that ran the intercept.



## <img class="os-logo" src="../../images/logo.png"/> What's Next?

<QSCards/>
