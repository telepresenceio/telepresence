# Kubewatch++

Living on branch `lomb/gorgonzola` (because names are hard and `kubewatch++` is no good).

## Starting Problem

We want to augment `kubewatch` to support Consul service "node" (read: endpoint) watches. We cannot watch the entire Consul service store. Instead with some configuration mechanism, initially one or more ConfigMaps and eventually "ConsulResolver" CustomResourceDefinition ("CRD"). Based on the config read during runtime we dynamically add or remove Consul service node watches.

The problem is that the current `kubewatch` makes it difficult to hook additional watches (Kubernetes or non-Kubernetes) onto previously discovered data. For example, we can find all the CRD's in the cluster but we cannot then easily have a piece of logic hook into changes on those CRDs so that Consul watches (or future "others") can easily be added.

## Additional Problem

Rafi also pointed out that one of the things that occurs in tests is that the Kubernetes API service gets hammered with watches. This because we attempt to watch every single endpoint in any watched namespace. This can (according to him) have signifigant performance impact especially on smaller dev/test clusters. However, if we make the watch configuration dynamic such that they can be discovered at runtime then we only need to watch endpoints we actually care about, for example, by inspecting Ambassador Mappings or a future Ambassador CRD.

# Theory of Operation

There are THREE major logical components in this design. All of these names tentative... as the prototype is built this may change, but mentally it looks like this.

- **Watch Controller** - Manage the creation and teardown of watches for **any** resource. This is meant to be pluggable but right now will focus on Kubernetes and Consul.

- **Assembler** - Creates a Snapshot and does two things:
    1. Makes the Snapshot available to Ambassador so Ambassador can do Ambassador things.
    2. Parses the Snapshot and generates a new list of sources for the Watch Controller. It then notifies the WatchController to update its running watches based on this configuration.

- **Subscription/Watch** - These are things watching some resource such as the Kubernetes API, the Consul service registry, or in future scenarios could be something like an AWS Lambda registry (food for thought).

```text
   
   +-----------------------------------+
   |CLI: Initial "static" watch sources|
   +-----------------------------------+
             |
             |
             | +----------------------+
             | |                      |
             V V                      |
    +-----------------+               |
    |                 |               |
    | WatchController |               | (updated Sources parsed from SNAPSHOT)
    |                 |               |
    +-----------------+               |
             | 1                      |            +-----+
             |                        |          +-|Timer|-+
             |                        |          | +-----+ |
             | N                      ^          |         |
      +------------+   NOTIFY   +-------------+  v   +-------------+        +-------------+
      |  Watchers  |----------->|  Aggregator |----->|  Filter     |------->|  Assembler  |--><SNAPSHOT>
      |            | N        1 |             |      |             |        |             |
      +------------+            +-------------+      +-------------+        +-------------+
```

## Notes from pow-wow with Phil

constraints:
 - we can't fire off the sync hook until the world is bootstrappped

 - we know the world is bootstrapped with respect to k8s because the
   k8s watcher won't invoke its callbacks until it is synced with the
   k8s api server

 - we need to compute when the world is bootstrapped with respect to
   consul by waiting until each consul service that we learn about
   from k8s has endpoint info
   + this does not mean populated endpoint info necessarily, it just
     means we have interacted with consul and have a recent view of
     what consul believes about the given service

 - we can't invoke overlapping sync hooks (or hook sets as the case
   may be), i.e. we need to wait until the prior hook (or hook sets)
   has finished before invoking another one

desirement/opinion:

 - rhs: it would be nice to not have this logic too distributed across
   multiple python/go processes... ideally it would be nice if
   watt+ambex were the only long running processes and the ambassador
   compiler could just be directly slotted in as a hook (as opposed to
   notifying another long running process that does the actual work)

 - this is not entirely in our control because there are other moving
   parts, but it would be nice if it were easy to use in this way

aggregator -> filter:

  func worldChanged(world World) {
     if world.NotBootstrapped() {
       return
     }

     delay := limiter.Limit(world.Timestamp())
     if delay == 0 {
        produceSnapshot(world)
     } else if delay > 0 {
        time.AfterFunc(answer, func() {
           worldChanged(world)
        })
     } else {
       // delay is less than zero so we drop the event entirely
       return
     }
  }

filter -> assembler:

  func produceSnapshot(world World) {
     blobl := world.Serialize()
     id := NewId()
     // save stuff
     invoke(id, blob)
  }

## ThOP Notes

1. "Watchers" is a generic catch all for Kubernetes/Consul/Whatever resource watcher. 
2. How the Assembler gets info about changes in the Watches is not important at this phase. They may push them over a Channel or the Assembler may query them somehow. It has not yet been decided. 

## Next Steps

 - ???