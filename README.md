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

It is a daemon that runs on your laptop (or anywhere else) and
provides three basic functions:

 * a DNS server that:
  - maintains records for dns-addressible entities in a configured
    kubernetes cluster
  - forwards any requests that don't match kubernetes entities to a
    configurable fallback

 * a SOCKS5 proxy that maintains auto-reconnecting connectivity into
   the the kubernetes cluster

 * a traffic capturing mechanism that:
  - dynamicaly adds/removes port forwarding rules for kubernetes ips
  - translates tcp connections to socks5 connections

Of these four basic functions, the first two have extremely minimal OS
and environmental dependencies. They are also useful on their own, For
example, many applications can be directly configured to use a SOCKS5
proxy. There are also various third party tools on both macos and
windows that claim to solve the problem of capturing some/all traffic
and sending it through a SOCKS5 proxy.

For the traffic capturing mechanism to work, there are two basic
primitives that are OS-specific:

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

Install the proxy endpoint into your cluster:

```
kubectl apply -f proxy.yaml
```

Step 2:

```
go get github.com/datawire/tp2/cmd/tp2
sudo tp2 -kubeconfig ~/.kube/config -dns $(fgrep nameserver /etc/resolv.conf | awk '{ print $2 }') -remote $(kubectl get svc sshd -o go-template='{{(index .status.loadBalancer.ingress 0).ip}}')
```

Note: If you are using the google cloud auth plugin for kubectl, then
at some point your tokens will expire and the plugin will try to
reauth. The reauth will fail because tp2 is not running as you but
running as root instead. There are probably better ways to fix this,
but the workaround I found was to make the tp2 binary suid root and
invoke it directly instead of via sudo, e.g.:

```
chown root:root $(which tp2)
chmod u+s $(which tp2)
tp2 -kubeconfig ~/.kube/config -dns $(fgrep nameserver /etc/resolv.conf | awk '{ print $2 }') -remote $(kubectl get svc sshd -o go-template='{{(index .status.loadBalancer.ingress 0).ip}}')
```

Step 3:

Now you should be able to access any kubernetes services:

```
...
curl -k https://kubernetes/api
...
```

To Do
-----

UX:

 - instead of supply dns manually we could watch resolv.conf for changes and load it automatically
 - instead of supply remote endpoint manually we could query it out of kubernetes
 - we could watch the supplied kubeconfig for changes... when combined with the prior two options and the suid installation, the invocation would require no args, e.g.:
   ```
   # automatically (re)connect me to whatever cluster my context points to
   tp2
   ```

Tests:

 - dns + routing from inside docker and outside docker
 - kill ssh and make sure it restarts
 - close laptop and make sure ssh restarts
 - move networks (with different config) and stuff continues to work
 - change dns and stuff continues to work
 - kill daemon and it cleans up after itself (ipt + ssh)
 - what does daemon do when starting dirty (with ipt, with old ssh, when ssh fails e.g. due to port conflict)
 - create/delete services and see that they appear/disappear
