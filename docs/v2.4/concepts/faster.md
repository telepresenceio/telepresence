# Making the remote local: Faster feedback, collaboration and debugging

With the goal of achieving [fast, efficient development](https://www.getambassador.io/use-case/local-kubernetes-development/), developers need a set of approaches to bridge the gap between remote Kubernetes clusters and local development, and reduce time to feedback and debugging.

## How should I set up a Kubernetes development environment?

[Setting up a development environment](https://www.getambassador.io/resources/development-environments-microservices/) for Kubernetes can be much more complex than the set up for traditional web applications. Creating and maintaining a Kubernetes development environment relies on a number of external dependencies, such as databases or authentication.

While there are several ways to set up a Kubernetes development environment, most introduce complexities and impediments to speed. The dev environment should be set up to easily code and test in conditions where a service can access the resources it depends on.

A good way to meet the goals of faster feedback, possibilities for collaboration, and scale in a realistic production environment is the "single service local, all other remote" environment. Developing in a fully remote environment offers some benefits, but for developers, it offers the slowest possible feedback loop. With local development in a remote environment, the developer retains considerable control while using tools like [Telepresence](../../quick-start/) to facilitate fast feedback, debugging and collaboration.

## What is Telepresence?

Telepresence is an open source tool that lets developers [code and test microservices locally against a remote Kubernetes cluster](../../quick-start/). Telepresence facilitates more efficient development workflows while relieving the need to worry about other service dependencies.

## How can I get fast, efficient local development?

The dev loop can be jump-started with the right development environment and Kubernetes development tools to support speed, efficiency and collaboration. Telepresence is designed to let Kubernetes developers code as though their laptop is in their Kubernetes cluster, enabling the service to run locally and be proxied into the remote cluster. Telepresence runs code locally and forwards requests to and from the remote Kubernetes cluster, bypassing the much slower process of waiting for a container to build, pushing it to registry, and deploying to production.

A rapid and continuous feedback loop is essential for productivity and speed; Telepresence enables the fast, efficient feedback loop to ensure that developers can access the rapid local development loop they rely on without disrupting their own or other developers' workflows. Telepresence safely intercepts traffic from the production cluster and enables near-instant testing of code, local debugging in production, and [preview URL](../../howtos/preview-urls/) functionality to share dev environments with others for multi-user collaboration.

Telepresence works by deploying a two-way network proxy in a pod running in a Kubernetes cluster. This pod proxies data from the Kubernetes environment (e.g., TCP connections, environment variables, volumes) to the local process. This proxy can intercept traffic meant for the service and reroute it to a local copy, which is ready for further (local) development.

The intercept proxy works thanks to context propagation, which is most frequently associated with distributed tracing but also plays a key role in controllable intercepts and preview URLs.
