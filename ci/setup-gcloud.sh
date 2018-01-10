#!/bin/bash
set -e

if ! type -p gcloud; then
    # Cannot find gcloud.  So we'll just install it.
    if [ ! -d "$HOME/google-cloud-sdk/bin" ]; then
	rm -rf $HOME/google-cloud-sdk;
	export CLOUDSDK_CORE_DISABLE_PROMPTS=1;
	curl https://sdk.cloud.google.com | bash;
    fi
    export PATH=~/google-cloud-sdk/bin:$PATH

    gcloud --quiet components update
    gcloud --quiet components update kubectl
fi

SERVICE_KEY=gcloud-service-key.json

if [ ! -e "${SERVICE_KEY}" ]; then
    echo "Provide gcloud service account key in ``${SERVICE_KEY}``"
    echo "Obtain one from GCP Console:"
    echo "    APIs & Services > Credentials > Create credentials > Service account key"
    exit 1
fi

gcloud --quiet version
gcloud auth activate-service-account --key-file "${SERVICE_KEY}"

PROJECT_NAME=$1
CLUSTER_NAME=$2
CLOUDSDK_COMPUTE_ZONE=$3

gcloud --quiet config set project $PROJECT_NAME
gcloud --quiet config set container/cluster $CLUSTER_NAME
gcloud --quiet config set compute/zone ${CLOUDSDK_COMPUTE_ZONE}
gcloud --quiet container clusters get-credentials $CLUSTER_NAME
