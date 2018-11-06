Goals for this PoC:

Robustness:

 - Connectivity, e.g. auto reconnect when connections die.

 - Consistency, i.e. always does the same thing in a given
   environment.

 - Environmental robustness, e.g. when it fails due to an
   environmental issue, it is obvious why it failed and it is obvious
   what steps need to be taken to get it to succeed.

 - Cleanup, i.e. no garbage.

Performance:

 - Start/stop as fast as possible.

Understandability:

 - Make it way easier to understand at a high level how telepresence
   works.

 - Make it easier for users/devs to understand and debug different
   failure modes.

 - Make it easier for users/devs to understand how telepresence
   integrates into its environment.

 - Make it easier for users/devs to integrate telepresence into new
   environments.

Portability:

 - Use the same basic design in as many different environments as
   possible.

 - Depend on a small number of well defined primitives that are
   available on every platform we care about.

How it works
------------

The go implementation consists of three components wired together in
such a way as to provide the illusion that a system is on the same
network as a kubernetes cluster.

The Parts:

  - An overlay dns server:

    This is a component that uses the github.com/miekg/dns library to
    provide a dns server implementation that resolves each query by
    first checking with "local logic", and then falling back to
    relaying to a remote dns server. This results in a dns server that
    will mimic any remote server modulo whatever overrides are
    implemented in the "local logic". Both the local logic and the
    remote dns server are configurable.

  - A firewall-based ip interceptor:

    This is a component that uses the system firewall (iptables on
    linux, pf on mac) to intercept connections made to specified ip
    addresses and/or CIDRs. Whenever a connection is made, a callback
    is invoked with the incoming connection along with the original
    destination.

  - A kubernetes event notifier:

    This is a component that watches and listens for interesting
    kubernetes events and notifies a supplied set of callbacks when
    they occur.

These three components run in a single daemon on your laptop (or
anywhere else) and together they provide these basic functions:

 * The DNS server:
   - maintains records for dns-addressible entities in your current
     kubernetes cluster
   - forwards any requests that don't match kubernetes entities to
     your original dns server

 * The ip interceptor forwards connections to a SOCKS5 proxy. The
   SOCKS5 proxy is also a tunnel into the cluster (one endpoint is
   local, the other endpoint is in the cluster). The SOCKS5 proxy is
   automatically restarted on failure.

 * Both of the above components depend on the view of dns-addressible
   entities maintained by the kubernetes event notifier.

For the ip interceptor to work, there are two basic primitives that
are OS-specific:

1. The ability to configure the OS to forward an ip (or range of ips)
   to a designated local ip/port:

2. The ability for whatever process is listening on the target of the
   forwarding rule to recover the original destination of the
   connection/packet.

   We *may* actually be able to provide a (slightly limited) form of
   traffic capturing *without* this second capability if we simply
   use (1) to forward each individual ip/port combo to a unique
   local address rather than forwarding all captured traffic to the
   same local address.

   The main issue I can think of with this scheme is that depending on
   how many IPs you have in your cluster, there may be more kubernetes
   ip/port combos than you have local ports available. A possible way
   to address this would be to only port forward the subset of IPs
   that have been DNS resolved. The main drawback I can think of for
   this approach is that if you were to cut and paste IPs from
   e.g. kubectl output, you would not be able to connect directly to
   them without doing a dns lookup first.

I know (1) and (2) exist on linux and macos. I have pretty high
confidence that (1) exists on windows in the form of `netsh`. I have
no idea if (2) exists on windows.

How to use
----------

Step 1:

```
go get github.com/datawire/teleproxy/cmd/teleproxy
sudo teleproxy -kubeconfig ~/.kube/config
```

Note: If you are using the google cloud auth plugin for kubectl, then
at some point your tokens will expire and the plugin will try to
reauth. The reauth will fail because teleproxy is not running as you but
running as root instead. There are probably better ways to fix this,
but the workaround I found was to make the teleproxy binary suid root and
invoke it directly instead of via sudo, e.g.:

```
sudo chown root:wheel $(which teleproxy)
sudo chmod u+s $(which teleproxy)
teleproxy
```

Step 2:

Now you should be able to access any kubernetes services:

```
...
curl -k https://kubernetes/api
...
```

Advanced Usage
--------------

The teleproxy binary provides a REST API as an integration and
inspection point. When teleproxy is running, it diverts requests to
http://teleproxy to a locally running REST API. You can see what
traffic teleproxy is intercepting like so:

```
# dump all routing tables
curl http://teleproxy/api/tables/
# dump a specific routing table
curl http://teleproxy/api/tables/<name>
```

You can use the API to shutdown teleproxy:

```
curl http://teleproxy/api/shutdown
```

If you want to run the intercepter and docker/kubernetes bridge
portion separately (this is useful for avoiding the suid binary thing
above, you can do it like so:

```
# as root
sudo teleproxy -mode intercept
# as user
teleproxy -mode bridge
```

You can extend teleproxy by adding additional routing tables, e.g.:

```
curl -X POST http://teleproxy/api/tables/ -d@- <<EOF
[{
  "name": "my-routing-table",
  "routes": [
    {"name": "myhostname", "proto": "tcp", "ip": "1.2.3.4", "target": "1234"},
    {"name": "myotherhostname", "proto": "tcp", "ip": "1.2.3.5", "target": "1234"}
  ]
}]
EOF
```

The above will cause teleproxy to resolve `myhostname` and
`myotherhostname` to `1.2.3.4`, and `1.2.3.5`, and divert traffic from
those ips to a socks5 proxy running on port "1234" (this is the socks5
proxy that is run by the kubernetes/docker bridge). If you wanted to
run your own socks5 proxy on a different port, you could supply that
instead.

Note that you can supply as many tables as you like with different
names. If you supply the name of an existing table, then *all* the
routes in the existing table are replaced with the routes in the
supplied table.

To Do
-----

UX:

 - instead of supply dns manually we could watch resolv.conf for changes and load it automatically
 - we could watch the supplied kubeconfig for changes... when combined with the prior two options and the suid installation, the invocation would require no args, e.g.:
   ```
   # automatically (re)connect me to whatever cluster my context points to
   teleproxy
   ```
Features:

 - Currently only kubernetes clusterIP services are supported. Need to
   watch other kinds of services and pods as well.
 - Right now all lookups are on the name as supplied. We need to
   support the proper lookup logic for
   "blah.namespace.svc.cluster.local".
 - Right now only A records are intercepted, should handle other
   types of DNS queries as well.

Diagnostics:

 - wiring together all the components in a way that allows them to quickly/easily report exactly why they don't work in any given environment might be a good strategy for improved diagnostics/bug reporting

Tests:

 - dns + routing from inside docker and outside docker
 - kill ssh and make sure it restarts
 - close laptop and make sure ssh restarts
 - move networks (with different config) and stuff continues to work
 - change dns and stuff continues to work
 - kill daemon and it cleans up after itself (ipt + ssh)
 - what does daemon do when starting dirty (with ipt, with old ssh, when ssh fails e.g. due to port conflict)
 - create/delete services and see that they appear/disappear
