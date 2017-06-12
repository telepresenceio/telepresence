
### Debugging a service locally with Telepresence

Imagine you have a service running in a staging cluster, and someone reports a bug against it.
In order to figure out the problem you want to run the service locally... but the service depends on other services in the cluster, and perhaps on cloud resources like a database.

In this tutorial you'll see how Telepresence allows you to debug your service locally.
We'll use the `telepresence` command line tool to swap out the version running in the staging cluster for a debug version under your control running on your local machine.
Telepresence will then forward traffic from {{ include.cluster }} to the local process.
