#!/bin/sh
set -e
set -x
# Run remote pod:
kubectl apply -f service.yaml
kubectl apply -f deployment.yaml
sleep 10 # Wait for pod to deploy

sudo docker run --rm --name=yourcode-deployment  -v $HOME/.kube:/opt/.kube:ro -v $HOME/.minikube:$HOME/.minikube:ro -v $PWD:/output datawire/local-telepresence $(id -u itamarst) yourcode-deployment 8080
