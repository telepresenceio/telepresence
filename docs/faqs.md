---
description: "Learn how Telepresence helps with fast development and debugging in your Kubernetes cluster."
---

# FAQs

** Why Telepresence?**

Modern microservices-based applications that are deployed into Kubernetes often consist of tens or hundreds of services. The resource constraints and number of these services means that it is often difficult to impossible to run all of this on a local development machine, which makes fast development and debugging very challenging. The fast [inner development loop](concepts/devloop/) from previous software projects is often a distant memory for cloud developers.

Telepresence enables you to connect your local development machine seamlessly to the cluster via a two way proxying mechanism. This enables you to code locally and run the majority of your services within a remote Kubernetes cluster -- which in the cloud means you have access to effectively unlimited resources.

Ultimately, this empowers you to develop services locally and still test integrations with dependent services or data stores running in the remote cluster.

You can “intercept” any requests made to a target Kubernetes workload, and code and debug your associated service locally using your favourite local IDE and in-process debugger. You can test your integrations by making requests against the remote cluster’s ingress and watching how the resulting internal traffic is handled by your service running locally.

** What operating systems does Telepresence work on?**

Telepresence currently works natively on macOS (Intel and Apple Silicon), Linux, and Windows.

** What protocols can be intercepted by Telepresence?**

Both TCP and UDP are supported for global intercepts.

Personal intercepts require HTTP. All HTTP/1.1 and HTTP/2 protocols can be intercepted. This includes:

- REST
- JSON/XML over HTTP
- gRPC
- GraphQL

If you need another protocol supported, please [drop us a line](https://github.com/telepresenceio/telepresence/issues/new?assignees=&labels=&projects=&template=Feature_request.md) to request it.

** When using Telepresence to intercept a pod, are the Kubernetes cluster environment variables proxied to my local machine?**

Yes, you can either set the pod's environment variables on your machine or write the variables to a file to use with Docker or another build process. Please see [the environment variable reference doc](reference/environment) for more information.

** When using Telepresence to intercept a pod, can the associated pod volume mounts also be mounted by my local machine?**

Yes, please see [the volume mounts reference doc](reference/volume/) for more information.

** When connected to a Kubernetes cluster via Telepresence, can I access cluster-based services via their DNS name?**

Yes. After you have successfully connected to your cluster via `telepresence connect -n <my_service_namespace>` you will be able to access any service in the connected namespace in your cluster via their DNS name.

This means you can curl endpoints directly e.g. `curl <my_service_name>:8080/mypath`.

You can also access services in other namespaces using their namespaced qualified name, e.g.`curl <my_service_name>.<my_other_namespace>:8080/mypath`.

You can connect to databases or middleware running in the cluster, such as MySQL, PostgreSQL and RabbitMQ, via their service name.

** When connected to a Kubernetes cluster via Telepresence, can I access cloud-based services and data stores via their DNS name?**

You can connect to cloud-based data stores and services that are directly addressable within the cluster (e.g. when using an [ExternalName](https://kubernetes.io/docs/concepts/services-networking/service/#externalname) Service type), such as AWS RDS, Google pub-sub, or Azure SQL Database.




** Will Telepresence be able to intercept workloads running on a private cluster or cluster running within a virtual private cloud (VPC)?**

Yes, but it doesn't need to have a publicly accessible IP address.

The cluster must also have access to an external registry in order to be able to download the traffic-manager and traffic-agent images that are deployed when connecting with Telepresence.

** Why does running Telepresence require sudo access for the local daemon unless it runs in a Docker container?**

The local daemon needs sudo to create a VIF (Virtual Network Interface) for outbound routing and DNS. Root access is needed to do that unless the daemon runs in a Docker container.

** What components get installed in the cluster when running Telepresence?**

A single `traffic-manager` service is deployed in the `ambassador` namespace within your cluster, and this manages resilient intercepts and connections between your local machine and the cluster.

A Traffic Agent container is injected per pod that is being intercepted. The first time a workload is intercepted all pods associated with this workload will be restarted with the Traffic Agent automatically injected.

** How can I remove all the Telepresence components installed within my cluster?**

You can run the command `telepresence helm uninstall` to remove everything from the cluster, including the `traffic-manager`, and all the `traffic-agent` containers injected into each pod being intercepted.

Also run `telepresence quit -s` to stop all local daemons running.

** What language is Telepresence written in?**

All components of the Telepresence application and cluster components are written using Go.

** How does Telepresence connect and tunnel into the Kubernetes cluster?**

The connection between your laptop and cluster is established by using
the `kubectl port-forward` machinery (though without actually spawning
a separate program) to establish a TLS encrypted connection to Telepresence
Traffic Manager and Traffic Agents in the cluster, and running Telepresence's custom VPN
protocol over that connection.

<a name="idps"></a>

** Is Telepresence OSS open source?**

Yes it is! You can find its source code on [GitHub](https://github.com/telepresenceio/telepresence).

** How do I share my feedback on Telepresence?**

Your feedback is always appreciated and helps us build a product that provides as much value as possible for our community. You can chat with us directly on our #telepresence-oss channel at the [CNCF Slack](https://slack.cncf.io).
