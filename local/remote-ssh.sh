#!/bin/sh
kubectl exec -i $1 nc localhost 22
