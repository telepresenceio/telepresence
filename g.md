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
             | 1                      |
             |                        |
             |                        |
             | N                      ^
      +------------+   NOTIFY   +-------------+
      |  Watchers  |----------->|  Assembler  |---> <SNAPSHOT>
      |            | N        1 |             |
      +------------+            +-------------+
```

## ThOP Notes

1. "Watchers" is a generic catch all for Kubernetes/Consul/Whatever resource watcher. 
2. How the Assembler gets info about changes in the Watches is not important at this phase. They may push them over a Channel or the Assembler may query them somehow. It has not yet been decided. 

## Next Steps

1. [Phil] Toy implementation of the diagram and description from above.
2. [Rafi, Phil] Poke at PoC. Decide if this path makes sense.
3. [Team] Assuming `<2>` is a success get broader feedback and sketch work to move from PoC to "real".