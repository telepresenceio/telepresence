
### Running a service locally with Telepresence

In this tutorial we'll show you how Telepresence allows you to forward traffic from {{ include.cluster }} to a local process.
Typically you'll have a version of your service already running the real code in your staging cluster.
We'll use the `telepresence` command line tool to swap out for a debug version under your control running on your local machine.
