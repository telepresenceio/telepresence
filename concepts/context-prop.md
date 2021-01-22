---
description: "Telepresence uses context propagation to intelligently route requests, transferring request metadata across the components of a distributed system."
---

# Context Propagation

Telepresence uses *context propagation* to intelligently route requests to the appropriate destination. Context propagation is transferring request metadata across the services and remote processes of a distributed system.

This metadata is the *context* that is transferred across the system services. It commonly takes the form of HTTP headers, such that context propagation is usually referred to as header propagation. A component of the system (like a proxy or performance monitoring tool) injects the headers into requests as it relays them.

The metadata *propagation* refers to any service or other middleware not stripping away the headers. Propagation facilitates the movement of the injected contexts between other downstream services and processes.

A common application for context propagation is *distributed tracing*. This is a technique for troubleshooting and profiling distributed microservices applications. In a microservices architecture, a single request may trigger additional requests to other services. The originating service may not cause the failure or slow request directly; a downstream dependent service may instead be to blame.

An application like Datadog or New Relic will use agents running on services throughout the system to inject traffic with HTTP headers (the context).  They will track the request’s entire path from origin to destination to reply, gathering data on routes the requests follow and performance. The injected headers follow the [W3C Trace Context specification](https://www.w3.org/TR/trace-context/), which facilitates maintaining the headers though every service without being stripped (the propagation).

Similarly, Telepresence also uses custom headers and header propagation. Our use case however is controllable intercepts and preview URLs instead of tracing. The headers facilitate the smart routing of requests either to live services in the cluster or services running on a developer’s machine.

Preview URLs, when created, generate an ingress request containing a custom header with a token (the context). Telepresence sends this token to Ambassador Cloud with other info about the preview. Visiting the preview URL directs the user to Ambassador Cloud, which proxies the user to the cluster ingress with the token header injected into the request. The request carrying the header is routed in the cluster to the appropriate pod (the propagation). The Traffic Agent on the service pod sees the header and intercepts the request, redirecting it to the local developer machine that ran the intercept.





