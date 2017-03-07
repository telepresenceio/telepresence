#!/bin/sh
set -e
set -x
# Run remote pod:
kubectl apply -f service.yaml
kubectl apply -f deployment.yaml
sleep 10 # Wait for pod to deploy

../cli/telepresence --expose 8080 --deployment yourcode-deployment -- --rm -i -t alpine /bin/sh
