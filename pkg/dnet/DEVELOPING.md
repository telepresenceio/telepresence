## Working on dnet

The `dnet` package contains a bunch of tests that are just running our
types through `nettest.TestConn`.  Unfortunately, in the event of a
failure, `nettest.TestConn` has proven a little tricky to debug.  Here
are my tips:

 - Unfortunately, the most common failure-mode seems to be "the test
   hangs" rather than "the test reports a failure".

 - With that in mind, I find it helpful to use `go test -timeout=15s`.

 - When a test times out, it prints a stack trace/thread dump.  One of
   my main debugging techniques has been to save that to a file, and
   annotate each goroutine with what it is ("klog runtime", "test
   main", "conn1.Read"), and so I can reason out which goroutine
   hanging is the "root" of the hang.

 - I also find it useful to temporarily add log statements to
   golang.org/x/net/nettest for whichever thing I'm debugging.

 - I find it helpful to use `go test
   -run=TestKubectlPortForward/Client/BasicIO` (or whichever subtest)
   to work on just one subtest at a time.
