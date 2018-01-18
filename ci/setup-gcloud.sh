#!/bin/bash -ex

PROJECT_NAME=$1
CLUSTER_NAME=$2
CLOUDSDK_COMPUTE_ZONE=$3
OS=$4

if ! type -p gcloud; then
    # Cannot find gcloud.  So we'll just install it.
    case "${OS}" in
	linux)
	    # Create an environment variable for the correct distribution
	    export CLOUD_SDK_REPO="cloud-sdk-$(lsb_release -c -s)"
	    # Add the Cloud SDK distribution URI as a package source
	    echo "deb http://packages.cloud.google.com/apt $CLOUD_SDK_REPO main" | sudo tee -a /etc/apt/sources.list.d/google-cloud-sdk.list
	    # Import the Google Cloud Platform public key
	    curl https://packages.cloud.google.com/apt/doc/apt-key.gpg | sudo apt-key add -
	    # Update the package list and install the Cloud SDK
	    sudo apt-get update && sudo apt-get install google-cloud-sdk
	    ;;

	*)
	    if [ ! -d "$HOME/google-cloud-sdk/bin" ]; then
		rm -rf $HOME/google-cloud-sdk;
		export CLOUDSDK_CORE_DISABLE_PROMPTS=1;
		curl https://sdk.cloud.google.com | bash;
	    fi
	    export PATH=~/google-cloud-sdk/bin:$PATH
	    ;;
    esac
fi

if ! type -p kubectl; then
    # Cannot find kubectl.  Install it.
    # https://kubernetes.io/docs/tasks/tools/install-kubectl/
    case "${OS}" in
	linux)
	    # Wish there were Ubuntu packages...
	    VER="v1.9.0"
	    curl -LO https://storage.googleapis.com/kubernetes-release/release/${VER}/bin/linux/amd64/kubectl
	    chmod +x ./kubectl
	    ;;

	osx)
	    brew install kubectl
	    ;;

	*)
	    echo "Unknown platform."
	    exit 1
	    ;;
    esac
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

gcloud --quiet config set project $PROJECT_NAME
gcloud --quiet config set container/cluster $CLUSTER_NAME
gcloud --quiet config set compute/zone ${CLOUDSDK_COMPUTE_ZONE}
gcloud --quiet container clusters get-credentials $CLUSTER_NAME
