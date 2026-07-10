---
title: FAQs
description: "Learn how Telepresence helps with fast development and debugging in your Kubernetes cluster."
hide_table_of_contents: true
---

# FAQs

### Why Telepresence

Modern microservices-based applications that are deployed into Kubernetes often consist of tens or hundreds of services. The resource constraints and number of these services means that it is often difficult to impossible to run all of this on a local development machine, which makes fast development and debugging very challenging. The fast inner development loop from previous software projects is often a distant memory for cloud developers.

Telepresence enables you to connect your local development machine seamlessly to the cluster via a two-way proxying mechanism. This enables you to code locally and run the majority of your services within a remote Kubernetes cluster — which in the cloud means you have access to effectively unlimited resources.

Ultimately, this empowers you to develop services locally and still test integrations with dependent services or data stores running in the remote cluster.

Telepresence provides four different ways for you to code, debug, and test your service locally using your favourite local IDE and in-process debugger: you can **replace** a remote container entirely, **intercept** the requests made to a service, **wiretap** a copy of a service's traffic, or **ingest** a container's environment and volumes without touching traffic. See [Attachments](concepts/attachments.md) for how the four modes compare and when to use which.

#### What operating systems does Telepresence work on?

Telepresence currently works natively on macOS, Linux, and Windows.

#### What architecture does Telepresence work on?

All Telepresence binaries are released for both AMD (Intel) and ARM (Apple Silicon) chips. 

#### What protocols can be intercepted by Telepresence?

Both TCP and UDP are supported.

#### When using Telepresence to run a cluster service locally, are the Kubernetes cluster environment variables proxied on my local machine?

Yes, you can either set the container's environment variables on your machine or write the variables to a file to use with Docker or another build process. You can also directly pass the environments to a handler that runs locally. Please see [the environment variable reference doc](reference/environment.md) for more information.

#### When using Telepresence to run a cluster service locally, can the associated container volume mounts also be mounted by my local machine?

Yes, and when running Docker, they can be used as docker volumes. Please see [the volume mounts reference doc](reference/volume.md) for more information.

#### When connected to a Kubernetes cluster via Telepresence, can I access cluster-based services via their DNS name?

Yes. After you have successfully connected to your cluster via `telepresence connect -n <my_service_namespace>` you will be able to access any service in the connected namespace in your cluster via their DNS name.

This means you can curl endpoints directly e.g. `curl <my_service_name>:8080/mypath`.

You can also access services in other namespaces using their namespaced qualified name, e.g.`curl <my_service_name>.<my_other_namespace>:8080/mypath`.

In essence, Telepresence makes the DNS of the connected namespace available locally. This means that you can connect to all databases or middleware running in the cluster, such as MySQL, PostgreSQL and RabbitMQ, via their service name.

#### When connected to a Kubernetes cluster via Telepresence, can I access cloud-based services and data stores via their DNS name?

You can connect to cloud-based data stores and services that are directly addressable within the cluster (e.g. when using an [ExternalName](https://kubernetes.io/docs/concepts/services-networking/service/#externalname) Service type), such as AWS RDS, Google pub-sub, or Azure SQL Database.

#### Will Telepresence be able to attach to workloads running on a private cluster or cluster running within a virtual private cloud (VPC)?

Yes. The cluster does not need to have a publicly accessible IP address.

The cluster must also have access to an external registry to be able to download the traffic-manager and traffic-agent images that are deployed when connecting with Telepresence.

#### Why does running Telepresence sometimes require sudo access for the local daemon?

The local daemon needs to create a VIF (Virtual Network Interface) for outbound routing and DNS, which is a privileged operation. However, sudo is **not** required when:

- Telepresence was installed using a [package installer](install/client.md) (`.pkg` on macOS, `.deb`/`.rpm` on Linux, or the Windows setup installer), which configures the root daemon as a system service.
- Telepresence runs in [Docker mode](howtos/docker.md) (`telepresence connect --docker`).

Sudo is only needed when using a standalone binary installation without a system service.

#### What components get installed in the cluster when running Telepresence?

A `traffic-manager` service is deployed in a namespace of your choice (default 'ambassador') within your cluster, and this manages attachments and connections between your local machine and the cluster.

A Traffic Agent container is injected per pod involved in an attachment. The injection happens the first time a `replace`, an `ingest`, or an `intercept` is made on a workload, unless you choose to control the injection using an annotation, in which case the injection happens when the `traffic-manager` is installed. When the cluster is configured to serve attachments with the node-agent (`nodeAgent.enabled=true` on the traffic-manager), nothing is injected — a node-hosted agent instead attaches to the existing pod, which is neither modified nor restarted. See the [node-agent reference](reference/node-agent.md) for details.

#### How can I remove all the Telepresence components installed within my cluster?

You can run the command `telepresence helm uninstall` to remove everything from the cluster, including the `traffic-manager`, all the `traffic-agent` containers injected into each attached pod, and any node-hosted agent Jobs.

You also can run the command `telepresence uninstall <workload>` to remove the injected `traffic-agent` containers injected into each pod for that workload.

Also run `telepresence quit -s` to stop all local daemons running.

#### What language is Telepresence written in?

All components of the Telepresence application and cluster components are written using Go. 

#### How does Telepresence connect and tunnel into the Kubernetes cluster?

The connection between your laptop and cluster is established by using
the `kubectl port-forward` machinery (though without actually spawning
a separate program) to establish a TLS encrypted connection to Telepresence
Traffic Manager and Traffic Agents in the cluster, and running Telepresence's custom VPN
protocol over that connection.

#### Is Telepresence OSS open source?

Yes, it is! You'll find both source code and documentation in the [Telepresence GitHub repository](https://github.com/telepresenceio/telepresence), licensed using the [Apache License Version 2.0](https://github.com/telepresenceio/telepresence?tab=License-1-ov-file#readme).

#### How do I share my feedback on Telepresence?

Your feedback is always appreciated and helps us build a product that provides as much value as possible for our community. You can chat with us directly on our #telepresence-oss channel at the [CNCF Slack](https://slack.cncf.io), and also report issues or create pull-requests on the GitHub repository.
