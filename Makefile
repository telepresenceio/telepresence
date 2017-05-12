.PHONY: default build-remote bumpversion release minikube-test build-remote-minikube

VERSION=$(shell git describe --tags)

default:
	@echo "See http://www.telepresence.io/additional-information/developing.html"

version:
	@echo $(VERSION)


## Setup dependencies ##

virtualenv:
	virtualenv --python=python3 virtualenv
	virtualenv/bin/pip install -r dev-requirements.txt
	virtualenv/bin/pip install -r remote/requirements.txt


## Development ##

# Build Docker image inside local Docker:
build-remote:
	cd remote && sudo docker build . -t datawire/telepresence-k8s:$(VERSION)

# Build Docker image inside minikube Docker:
build-remote-minikube:
	eval $(shell minikube docker-env) && \
		cd remote && \
		docker build . -q -t datawire/telepresence-k8s:$(VERSION)

# Run tests in minikube:
minikube-test: virtualenv build-remote-minikube
	@echo "IMPORTANT: this will change kubectl context to minikube!\n\n"
	kubectl config use-context minikube
	env TELEPRESENCE_VERSION=$(VERSION) ci/test.sh


## Release ##

# This is run by developer and triggers release process in CI:
bumpversion: virtualenv
	virtualenv/bin/bumpversion --verbose --list minor
	@echo "Please run: git push origin master --tags"

# Will be run in Travis CI on tagged commits
release: build-remote
	sudo docker push datawire/telepresence-k8s:$(VERSION)
	env TELEPRESENCE_VERSION=$(VERSION) packaging/homebrew-package.sh
	packaging/create-linux-packages.py $(VERSION)
	packaging/upload-linux-packages.py $(VERSION)
