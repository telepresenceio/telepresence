# Context propagation

**Context propagation** is the transfer of request metadata across the services and remote processes of a distributed system. Telepresence uses context propagation to intelligently route requests to the appropriate destination.

This metadata is the context that is transferred across system services. It commonly takes the form of HTTP headers; context propagation is usually referred to as header propagation. A component of the system (like a proxy or performance monitoring tool) injects the headers into requests as it relays them.

Metadata propagation refers to any service or other middleware not stripping away the headers. Propagation facilitates the movement of the injected contexts between other downstream services and processes.


## What is distributed tracing?

Distributed tracing is a technique for troubleshooting and profiling distributed microservices applications and is a common application for context propagation. It is becoming a key component for debugging.

In a microservices architecture, a single request may trigger additional requests to other services. The originating service may not cause the failure or slow request directly; a downstream dependent service may instead be to blame.

An application like Datadog or New Relic will use agents running on services throughout the system to inject traffic with HTTP headers (the context). They will track the request’s entire path from origin to destination to reply, gathering data on routes the requests follow and performance. The injected headers follow the [W3C Trace Context specification](https://www.w3.org/TR/trace-context/) (or another header format, such as [B3 headers](https://github.com/openzipkin/b3-propagation)), which facilitates maintaining the headers through every service without being stripped (the propagation).


## What are intercepts and preview URLs?

[Intercepts](../../reference/intercepts) and [preview
URLs](../../howtos/preview-urls/) are functions of Telepresence that
enable easy local development from a remote Kubernetes cluster and
offer a preview environment for sharing and real-time collaboration.

Telepresence uses custom HTTP headers and header propagation to
identify which traffic to intercept both for plain personal intercepts
and for personal intercepts with preview URLs; these techniques are
more commonly used for distributed tracing, so what they are being
used for is a little unorthodox, but the mechanisms for their use are
already widely deployed because of the prevalence of tracing.  The
headers facilitate the smart routing of requests either to live
services in the cluster or services running locally on a developer’s
machine. The intercepted traffic can be further limited by using path
based routing.

Preview URLs, when created, generate an ingress request containing a custom header with a token (the context). Telepresence sends this token to [Ambassador Cloud](https://app.getambassador.io) with other information about the preview. Visiting the preview URL directs the user to Ambassador Cloud, which proxies the user to the cluster ingress with the token header injected into the request. The request carrying the header is routed in the cluster to the appropriate pod (the propagation). The Traffic Agent on the service pod sees the header and intercepts the request, redirecting it to the local developer machine that ran the intercept.
