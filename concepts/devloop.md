---
description: "Inner and outer dev loops describe the processes developers repeat to iterate on code. As these loops get more complex, productivity decreases."
---

# Inner and Outer Dev Loops

Cloud native technologies also fundamentally altered the developer experience. Not only are engineers now expected to design and build distributed service-based applications, but their entire development loop has been disrupted. No longer can developers rely on monolithic application development best practices, such as checking out the entire codebase and coding locally with a rapid “live-reload” inner developer loop. They now have to manage external dependencies, build containers, and implement orchestration configuration (e.g. Kubernetes YAML). This may appear trivial at first glance, but this has a large impact on development time.

If a typical developer codes for 360 minutes (6 hours) a day, with a traditional local iterative development loop of 5 minutes -- 3 coding, 1 building i.e. compiling/deploying/reloading, 1 testing inspecting, and 10-20 seconds for committing code -- they can expect to make ~70 iterations of their code per day. Any one of these iterations could be a release candidate. The only “developer tax” being paid here is for the commit process, which is negligible.

![Traditional inner dev loop](../../../images/trad-inner-dev-loop.png)

If the build time is incremented to 5 minutes -- not atypical with a standard container build, registry upload, and deploy -- then the number of possible development iterations per day drops to ~40. At the extreme that’s a 40% decrease in potential new features being released. This new container build step is a hidden tax, which is quite expensive.

![Container inner dev loop](../../../images/container-inner-dev-loop.png)

Many development teams began using custom proxies to either automatically and continually sync their local development code base with a remote surrogate (enabling “live reload” in a remote cluster), or route all remote service traffic to their local services for testing. The former approach had limited value for compiled languages, and the latter often did not support collaboration within teams where multiple users want to work on the same services.

In addition to the challenges with the inner development loop, the changing outer development loop also caused issues. Over the past 20 years, end users and customers have become more demanding, but also less sure of their requirements. Pioneered by disruptive organizations like Netflix, Spotify, and Google, this has resulted in software delivery teams needing to be capable of rapidly delivering experiments into production. Unit, integration, and component testing is still vitally important, but modern application platforms must also support the incremental release of functionality and applications to end users in order to allow testing in production.

The traditional outer development loop for software engineers of code merge, code review, build artifact, test execution, and deploy has now evolved. A typical modern outer loop now consists of code merge, automated code review, build artifact and container, test execution, deployment, controlled (canary) release, and observation of results. If a developer doesn’t have access to self-service configuration of the release then the time taken for this outer loop increases by at least an order of magnitude e.g. 1 minute to deploy an updated canary release routing configuration versus 10 minutes to raise a ticket for a route to be modified via the platform team.