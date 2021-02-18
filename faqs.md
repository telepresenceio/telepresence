---
description: "Learn how Telepresence helps with fast development and debugging in your Kubernetes cluster."
---

# FAQs

** Why Telepresence?**

Modern microservices-based applications that are deployed into Kubernetes often consist of tens or hundreds of services. The resource constraints and number of these services means that it is often difficult to impossible to run all of this on a local development machine, which makes fast development and debugging very challenging. The fast [inner development loop](../concepts/devloop/) from previous software projects is often a distant memory for cloud developers.

Telepresence enables you to connect your local development machine seamlessly to the cluster via a two way proxying mechanism. This enables you to code locally and run the majority of your services within a remote Kubernetes cluster -- which in the cloud means you have access to effectively unlimited resources.

Ultimately, this empowers you to develop services locally and still test integrations with dependent services or data stores running in the remote cluster.

You can “intercept” any requests made to a target Kubernetes deployment, and code and debug your associated service locally using your favourite local IDE and in-process debugger. You can test your integrations by making requests against the remote cluster’s ingress and watching how the resulting internal traffic is handled by your service running locally.

By using the preview URL functionality you can share access with additional developers or stakeholders to the application via an entry point associated with your intercept and locally developed service. You can make changes that are visible in near real-time to all of the participants authenticated and viewing the preview URL. All other viewers of the application entrypoint will not see the results of your changes.

** What protocols can be intercepted by Telepresence?**

All HTTP/1.1 and HTTP/2 protocols can be intercepted. This includes:

- REST
- JSON/XML over HTTP
- gRPC
- GraphQL

If you need another protocol supported, please [drop us a line](../../../../feedback) to request it.

** When using Telepresence to intercept a pod, are the Kubernetes cluster environment variables proxied to my local machine?**

Yes, please see [this document](../reference/environment/) for more information.

** When using Telepresence to intercept a pod, are the associated pod volume mounts also proxied and shared with my local machine?**

Yes, please see [this doc on using volume mounts](../reference/volume/).

** When connected to a Kubernetes cluster via Telepresence, can I access cluster-based services via their DNS name?**

Yes. After you have successfully connected to your cluster via `telepresence connect` you have full access to the cluster namespace.

This means you can issue commands, such as `nslookup <my_service_name>`, and curl endpoints directly e.g. `curl <my_service_name>:8080/mypath`.

You can connect to databases or middleware running in the cluster, such as MySQL, PostgreSQL and RabbitMQ, via their service name.

** When connected to a Kubernetes cluster via Telepresence, can I access cloud-based services and data stores via their DNS name?**

You can connect to cloud-based data stores and services that are directly addressable within the cluster (e.g. when using an [ExternalName](https://kubernetes.io/docs/concepts/services-networking/service/#externalname) Service type), such as AWS RDS, Google pub-sub, or Azure SQL Database.

** What types of ingress does Telepresence support for the preview URL functionality?**

The preview URL functionality should work with most ingress configurations, including straightforward load balancer setups.

Telepresence will discover/prompt during first use for this info and make its best guess at figuring this out and ask you to confirm or update this.

** Will Telepresence be able to intercept deployments running on a private cluster or cluster running within a virtual private cloud (VPC)?**

Yes. The cluster has to have outbound access to the internet for the preview URLs to function correctly, but it doesn’t need to have a publicly accessible IP address.

The cluster must also have access to an external registry in order to be able to download the Traffic Manager and Traffic Agent containers that are deployed when connecting with Telepresence.

** Why does running Telepresence require sudo access for the local daemon?**

The local daemon needs sudo to create iptable mappings. Telepresence uses this to create outbound access from the laptop to the cluster.

On Fedora, Telepresence also creates a virtual network device (a TUN network) for DNS routing. That also requires root access.

** What components get installed in the cluster when running Telepresence?**

A single Traffic Manager service is deployed in the `ambassador` namespace within your cluster, and this manages resilient intercepts and connections between your local machine and the cluster.

A Traffic Agent container is injected per pod that is being intercepted. The first time a deployment is intercepted all pods associated with this deployment will be restarted with the Traffic Agent automatically injected.

** How can I remove all of the Telepresence components installed within my cluster?**

You can run the command `telepresence uninstall --everything` to remove the Traffic Manager service installed in the cluster and Traffic Agent containers injected into each pod being intercepted.

Running this command will also stop the local daemon running.

** What language is Telepresence written in?**

All components of the Telepresence application and cluster components are written using Go.

** How does Telepresence connect and tunnel into the Kubernetes cluster?**

The connection between your laptop and cluster is established via the standard `kubectl` mechanisms and SSH tunnelling.

** What identity providers are supported for authenticating to view a preview URL?**

Currently GitHub is used to authenticate a user of Telepresence (triggered via the `telepresence login` command) and any viewers of a preview URL.

More authentication mechanisms and identity provider support will be added soon. Please [let us know](../../../../feedback) which providers are the most important to you and your team in order for us to prioritize those.

** Is Telepresence open source?**

Telepresence will be open source soon, in the meantime it is free to download. We prioritized releasing the binary as soon as possible for community feedback, but are actively working on the open sourcing logistics.

** How do I share my feedback on Telepresence?**

Your feedback is always appreciated and helps us build a product that provides as much value as possible for our community. You can chat with us directly on our [feedback page](../../../../feedback), or you can [join our Slack channel](http://d6e.co/slack) to share your thoughts.
