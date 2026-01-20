---
title: Dealing With Conflicting Engagements
---

# Dealing With Conflicting Engagements

## The Problem
An organization may have several developers working on the same project, which in turn means that they may be engaging with the same workloads at the same time. Intercepting with unique http-header filters is often a good way to deal with this, but in some cases it may be necessary to use a global intercept or even to replace the entire container. Also, in some cases, perhaps a user intercepts using an http-header filter that is too broad and therefore causes conflicts with other users.

Sometimes the conflict is unavoidable. The user owning the first intercept must simply finish their work in order for others to continue. However, in other cases, perhaps that user has gone home for the day or got distracted by other tasks, not realizing that their intercept is still active and might cause problems for others.

## The Solution
Telepresence handles this situation in three ways:
1. An inactive client has a limited time to live before the session is terminated. The termination is performed by the Traffic Manager but is similar to a user disconnecting using `telepresence quit`. This timeout is controlled by Helm chart value `grpc.connectionTTL` and defaults to 24 hours. 
2. An inactive client will normally retain ongoing intercepts for the duration of the `grpc.connectionTTL`, but the right to block other intercept attempts will be lost after a shorter timeout period controlled by Helm chart value `intercept.inactiveBlockTimeout`. This defaults to 10 minutes.
3. In some cases, a client may be considered active even though no user is behind the keyboard (see [Active Client Semantics](#active-client-semantics) below). The command `telepresence revoke <intercept-id>` can be used to terminate such an intercept. Since this command is somewhat intrusive, it can only be performed by users that have RBAC permissions to get and update the "traffic-manager" configmap in the traffic-manager's namespace.

This means that a user may well close the lid on their laptop and come back the next day to continue working on their intercept, but only if that intercept didn't cause conflicts with other users. If it did, then it will be flagged as a conflict and the other user's attempt will instead succeed.

## Active Client Semantics
A client is considered active as long as the user interacts with Telepresence. Either by using the CLI or by issuing TCP network requests that trigger the creation of new tunnels to the cluster. Neither UDP network requests - which often originate from DNS requests that aren't related to Telepresence - nor inbound connections initiated from an intercepted pod, will make the client active.

> [!NOTE]
> The client will remain active if it runs processes that periodically use the Telepresence VIF to send TCP traffic to the cluster. 
