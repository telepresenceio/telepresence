# The developer experience and the inner dev loop

## How is the developer experience changing?

The developer experience is the workflow a developer uses to develop, test, deploy, and release software.

Typically this experience has consisted of both an inner dev loop and an outer dev loop. The inner dev loop is where the individual developer codes and tests, and once the developer pushes their code to version control, the outer dev loop is triggered.

The outer dev loop is _everything else_ that happens leading up to release. This includes code merge, automated code review, test execution, deployment, [controlled (canary) release](https://www.getambassador.io/docs/argo/latest/concepts/canary/), and observation of results. The modern outer dev loop might include, for example, an automated CI/CD pipeline as part of a [GitOps workflow](https://www.getambassador.io/docs/argo/latest/concepts/gitops/#what-is-gitops) and a progressive delivery strategy relying on automated canaries, i.e. to make the outer loop as fast, efficient and automated as possible.

Cloud-native technologies have fundamentally altered the developer experience in two ways: one, developers now have to take extra steps in the inner dev loop; two, developers need to be concerned with the outer dev loop as part of their workflow, even if most of their time is spent in the inner dev loop.

Engineers now must design and build distributed service-based applications _and_ also assume responsibility for the full development life cycle. The new developer experience means that developers can no longer rely on monolithic application developer best practices, such as checking out the entire codebase and coding locally with a rapid “live-reload” inner development loop. Now developers have to manage external dependencies, build containers, and implement orchestration configuration (e.g. Kubernetes YAML). This may appear trivial at first glance, but this adds development time to the equation.

## What is the inner dev loop?

The inner dev loop is the single developer workflow. A single developer should be able to set up and use an inner dev loop to code and test changes quickly.

Even within the Kubernetes space, developers will find much of the inner dev loop familiar. That is, code can still be written locally at a level that a developer controls and committed to version control.

In a traditional inner dev loop, if a typical developer codes for 360 minutes (6 hours) a day, with a traditional local iterative development loop of 5 minutes — 3 coding, 1 building, i.e. compiling/deploying/reloading, 1 testing inspecting, and 10-20 seconds for committing code — they can expect to make ~70 iterations of their code per day. Any one of these iterations could be a release candidate. The only “developer tax” being paid here is for the commit process, which is negligible.

![traditional inner dev loop](../../images/trad-inner-dev-loop.png)

## In search of lost time: How does containerization change the inner dev loop?

The inner dev loop is where writing and testing code happens, and time is critical for maximum developer productivity and getting features in front of end users. The faster the feedback loop, the faster developers can refactor and test again.

Changes to the inner dev loop process, i.e., containerization, threaten to slow this development workflow down. Coding stays the same in the new inner dev loop, but code has to be containerized. The _containerized_ inner dev loop requires a number of new steps:

* packaging code in containers
* writing a manifest to specify how Kubernetes should run the application (e.g., YAML-based configuration information, such as how much memory should be given to a container)
* pushing the container to the registry
* deploying containers in Kubernetes

Each new step within the container inner dev loop adds to overall development time, and developers are repeating this process frequently. If the build time is incremented to 5 minutes — not atypical with a standard container build, registry upload, and deploy — then the number of possible development iterations per day drops to ~40. At the extreme that’s a 40% decrease in potential new features being released. This new container build step is a hidden tax, which is quite expensive.


![container inner dev loop](../../images/container-inner-dev-loop.png)

## Tackling the slow inner dev loop

A slow inner dev loop can negatively impact frontend and backend teams, delaying work on individual and team levels and slowing releases into production overall.

For example:

* Frontend developers have to wait for previews of backend changes on a shared dev/staging environment (for example, until CI/CD deploys a new version) and/or rely on mocks/stubs/virtual services when coding their application locally. These changes are only verifiable by going through the CI/CD process to build and deploy within a target environment.
* Backend developers have to wait for CI/CD to build and deploy their app to a target environment to verify that their code works correctly with cluster or cloud-based dependencies as well as to share their work to get feedback.

New technologies and tools can facilitate cloud-native, containerized development. And in the case of a sluggish inner dev loop, developers can accelerate productivity with tools that help speed the loop up again.
