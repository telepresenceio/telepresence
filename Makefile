.PHONY: default build build-local build-remote bumpversion release

VERSION=0.0

default:
	@echo "To release:"
	@echo "1. 'make bumpversion'"
	@echo "2. do git push as instructed by bumpversion"
	@echo "3. 'make release'"

build: build-local build-remote

build-local:
	cd local && docker build . -t datawire/telepresence-local:$(VERSION)

build-remote:
	cd remote && docker build . -t datawire/telepresence-k8s:$(VERSION)

virtualenv:
	virtualenv virtualenv

bumpversion: virtualenv
	virtualenv/bin/pip install bumpversion
	virtualenv/bin/bumpversion --verbose --list minor

release: build
	docker push datawire/telepresence-local:$(VERSION)
	docker push datawire/telepresence-k8s:$(VERSION)
