---
description: "Install Telepresence and learn to use it to intercept services running in your Kubernetes cluster, speeding up local development and debugging."
---

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
import { UserInterceptCommand, DemoClusterWarning } from '../../../../../src/components/Docs/Telepresence';


# Telepresence Quick Start - **Go**

This guide provides you with a hands-on tutorial with Telepresence and Golang. To go through this tutorial, the only thing you'll need is a computer that runs Docker Desktop >=20.10.7. We'll give you a pre-configured remote Kubernetes cluster and a Docker container to run locally.

If you don't have Docker Desktop already installed, go to the [Docker download page](https://www.docker.com/get-started) and install Docker.

## 1. Get a free remote cluster

Telepresence connects your local workstation with a remote Kubernetes cluster. In this tutorial, you'll start with a pre-configured, remote cluster.

1. <Login urlParams="docs_source=telepresence-quick-start&login_variant=free-cluster-activation" origin="telepresence-novice-go-quick-start" />
2. Go to the <DCPLink>Service Catalog</DCPLink> to see all the services deployed on your cluster.
   <EmojivotoServicesList/>
    The Service Catalog gives you a consolidated view of all your services across development, staging, and production. After exploring the Service Catalog, continue with this tutorial to test the application in your demo cluster.

<DemoClusterWarning />

<div className="docs-opaque-section">

## 2. Try the Emojivoto application

The remote cluster is running the Emojivoto application, which consists of four services.
Test out the application:

1. Go to the <ExternalIp>Emojivoto webapp</ExternalIp> and vote for some emojis.
   <Alert severity="info">
   If the link to the remote demo cluster doesn't work, make sure you don't have an <strong>ad blocker</strong> preventing it from opening.
   </Alert>

2. Now, click on the 🍩 emoji. You'll see that a bug is present, and voting 🍩 doesn't work.

## 3. Run the Docker container

The bug is present in the `voting-svc` service, you'll run that service locally. To save your time, we prepared a Docker container with this service running and all you'll need to fix the bug.

1. Run the Docker container locally, by running this command inside your local terminal:

  <Platform.TabGroup>
    <Platform.MacOSTab>
      <DockerCommand osType="macos" language="go" />
    </Platform.MacOSTab>

    <Platform.GNULinuxTab>
      <DockerCommand osType="linux" language="go" />
    </Platform.GNULinuxTab>

    <Platform.WindowsTab>
      <DockerCommand osType="windows" language="go" />
    </Platform.WindowsTab>
  </Platform.TabGroup>

2. The application is failing due to a little bug inside this service which uses gRPC to communicate with the others services. We can use `grpcurl` to test the gRPC endpoint and see the error by running:

  ```
  $ grpcurl -v -plaintext -import-path ./proto -proto Voting.proto localhost:8081 emojivoto.v1.VotingService.VoteDoughnut

    Resolved method descriptor:
    rpc VoteDoughnut ( .emojivoto.v1.VoteRequest ) returns ( .emojivoto.v1.VoteResponse );

    Request metadata to send:
    (empty)

    Response headers received:
    (empty)

    Response trailers received:
    content-type: application/grpc
    Sent 0 requests and received 0 responses
    ERROR:
      Code: Unknown
      Message: ERROR
  ```

3. In order to fix the bug, use the Docker container's embedded IDE to fix this error. Go to <a href="http://localhost:8083" target="_blank">http://localhost:8083</a> and open `api/api.go`. Remove the `"fmt"` package by deleting the line 5.

  ```go
  3 import (
  4  "context"
  5  "fmt"     // DELETE THIS LINE
  6
  7  pb "github.com/buoyantio/emojivoto/emojivoto-voting-svc/gen/proto"
  ```

  and also replace the line `21`:

  ```go
  20 func (pS *PollServiceServer) VoteDoughnut(_ context.Context, _ *pb.VoteRequest) (*pb.VoteResponse, error) {
  21   return nil, fmt.Errorf("ERROR")
  22 }
  ```
  with
  ```go
  20 func (pS *PollServiceServer) VoteDoughnut(_ context.Context, _ *pb.VoteRequest) (*pb.VoteResponse, error) {
  21   return pS.vote(":doughnut:")
  22 }
  ```
  Then save the file (`Ctrl+s` for Windows, `Cmd+s` for Mac or `Menu -> File -> Save`) and verify that the error is fixed now:

  ```
  $ grpcurl -v -plaintext -import-path ./proto -proto Voting.proto localhost:8081 emojivoto.v1.VotingService.VoteDoughnut

    Resolved method descriptor:
    rpc VoteDoughnut ( .emojivoto.v1.VoteRequest ) returns ( .emojivoto.v1.VoteResponse );

    Request metadata to send:
    (empty)

    Response headers received:
    content-type: application/grpc

    Response contents:
    {
    }

    Response trailers received:
    (empty)
    Sent 0 requests and received 1 response
  ```

## 4. Telepresence intercept

1. Now the bug is fixed, you can use Telepresence to intercept *all* the traffic through our local service.
Run the following command inside the container:

  ```
  $ telepresence intercept voting --port 8081:8080

    Using Deployment voting
    intercepted
      Intercept name         : voting
      State                  : ACTIVE
      Workload kind          : Deployment
      Destination            : 127.0.0.1:8081
      Service Port Identifier: 8080
      Volume Mount Point     : /tmp/telfs-XXXXXXXXX
      Intercepting           : all TCP connections
  ```
  Now you can go back to <ExternalIp>Emojivoto webapp</ExternalIp> and you'll see that voting for 🍩 woks as expected.

You have created an intercept to tell Telepresence where to send traffic. The `voting-svc` traffic is now destined to the local Dockerized version of the service. This intercepts *all the traffic* to the local `voting-svc` service, which has been fixed with the Telepresence intercept.

<Alert severity="success">
  <strong>Congratulations!</strong> Traffic to the remote service is now being routed to your local laptop, and you can see how the local fix works on the remote environment!
</Alert>

## 5. Telepresence intercept with a preview URL

Preview URLs allows you to safely share your development environment. With this approach, you can try and test your local service more accurately because you have a total control about which traffic is handled through your service, all of this thank to the preview URL. 

1. First leave the current intercept: 

  ```
  $ telepresence leave voting
  ```

2. Then login to telepresence: 

  <LoginCommand />

3. Create an intercept, which will tell Telepresence to send traffic to the service in our container instead of the service in the cluster. When prompted for ingress configuration, all default values should be correct as displayed below.

  <UserInterceptCommand language="go" />

4. If you access the <ExternalIp>Emojivoto webapp</ExternalIp> application on your remote cluster and vote for the 🍩 emoji, you'll see the bug is still present.

5. Vote for the 🍩 emoji using the <PreviewUrl language="go">Preview URL</PreviewUrl> obtained in the previous step, and you will see that the bug is fixed, since traffic is being routed to the fixed version which is running locally.

</div>

## <img class="os-logo" src="../../images/logo.png"/> What's Next?

You've intercepted a service in one of our demo clusters, now you can use Telepresence to [intercept a service in your own environment](https://www.getambassador.io/docs/telepresence/latest/howtos/intercepts/)!
