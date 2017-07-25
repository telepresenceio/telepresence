.PHONY: default build-k8s-proxy bumpversion release minikube-test build-k8s-proxy-minikube setup

VERSION=$(shell git describe --tags)
SHELL:=/bin/bash

default:
	@echo "See http://www.telepresence.io/additional-information/developing.html"

version:
	@echo $(VERSION)


## Setup dependencies ##

virtualenv:
	virtualenv --python=python3 virtualenv
	virtualenv/bin/pip install -r dev-requirements.txt
	virtualenv/bin/pip install -r k8s-proxy/requirements.txt

virtualenv/bin/sshuttle-telepresence: virtualenv
	source virtualenv/bin/activate && packaging/build-sshuttle.py

setup: virtualenv/bin/sshuttle-telepresence

# Build Kubernetes side proxy image inside local Docker:
build-k8s-proxy:
	cd k8s-proxy && sudo docker build . -t datawire/telepresence-k8s:$(VERSION)

build-local:
	cp -f virtualenv/bin/sshuttle-telepresence local-docker
	cp -f cli/telepresence local-docker/telepresence.py
	cd local-docker && sudo docker build . -t datawire/telepresence-local:$(VERSION)
	rm -f local-docker/sshuttle-telepresence local-docker/telepresence.py

## Development ##

# Build Docker image inside minikube Docker:
build-k8s-proxy-minikube:
	ci/build-k8s-proxy-minikube.sh

build-k8s-proxy-minishift:
	eval $(shell minishift docker-env) && \
		cd k8s-proxy && \
		docker build . -q -t datawire/telepresence-k8s:$(VERSION)

run-minikube:
	source virtualenv/bin/activate && \
		env TELEPRESENCE_VERSION=$(VERSION) cli/telepresence --method=inject-tcp --new-deployment test --run-shell

# Run tests in minikube:
minikube-test: virtualenv build-k8s-proxy-minikube build-local
	@echo "IMPORTANT: this will change kubectl context to minikube!\n\n"
	kubectl config use-context minikube
	TELEPRESENCE_VERSION=$(VERSION) TELEPRESENCE_METHOD=container ci/test.sh
	source virtualenv/bin/activate && \
		env TELEPRESENCE_VERSION=$(VERSION) TELEPRESENCE_METHOD=inject-tcp ci/test.sh
	source virtualenv/bin/activate && \
		env TELEPRESENCE_VERSION=$(VERSION) TELEPRESENCE_LOCAL_VM=1 \
		TELEPRESENCE_METHOD=vpn-tcp ci/test.sh

# Run tests relevant to OpenShift:
openshift-tests: virtualenv
	source virtualenv/bin/activate && \
		env TELEPRESENCE_OPENSHIFT=1 TELEPRESENCE_VERSION=$(VERSION) \
		TELEPRESENCE_METHOD=inject-tcp ci/test.sh
	source virtualenv/bin/activate && \
		env TELEPRESENCE_OPENSHIFT=1 TELEPRESENCE_METHOD=vpn-tcp \
		TELEPRESENCE_LOCAL_VM=1 TELEPRESENCE_VERSION=$(VERSION) ci/test.sh

## Release ##

# This is run by developer and triggers release process in CI:
bumpversion: virtualenv
	virtualenv/bin/bumpversion --verbose --list minor
	@echo "Please run: git push origin master --tags"

# Will be run in Travis CI on tagged commits
release: build-k8s-proxy build-local virtualenv/bin/sshuttle-telepresence
	sudo docker push datawire/telepresence-k8s:$(VERSION)
	sudo docker push datawire/telepresence-local:$(VERSION)
	env TELEPRESENCE_VERSION=$(VERSION) packaging/homebrew-package.sh
	packaging/create-linux-packages.py $(VERSION)
	packaging/upload-linux-packages.py $(VERSION)
