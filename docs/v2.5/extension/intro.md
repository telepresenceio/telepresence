# Telepresence for Docker

## What is Telepresence for Docker?

Telepresence for Docker runs entirely within containers. The Telepresence Daemons run in a container, which can be given commands using the extension UI. When Telepresence intercepts a service, it redirects cloud traffic to other containers on the docker host network.

## What is Telepresence for Docker good at?

Telepresence for Docker is isolated from the user's machine; it operates entirely within the docker runtime. Therefore, Telepresence for Docker does not require root permission on the user's machine.

## How does Telepresence for Docker work?

Telepresence for Docker is configured to use Docker's host network (VM network for Windows and Mac, host network on Linux). Normally, docker containers are isolated from echother, however, containers can be configured to share a network, if they are both configured to use Docker's host network.
