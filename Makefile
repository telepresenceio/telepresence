# Copyright 2018 Datawire. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# If switching from docker.io back to gcr.io, then you must also
# uncomment .circleci/config.yml's 'build-image' job's gcloud
# authentication.
TELEPRESENCE_REGISTRY ?= docker.io/datawire
#TELEPRESENCE_REGISTRY ?= gcr.io/$(PROJECT_NAME)
VERSION_SUFFIX        ?= -$(OS)-$(TIME)
DOCKER_PUSH           ?= docker-push
PYTEST_ARGS           ?=

#

ifeq ($(filter check,$(or $(MAKECMDGOALS),default)),check)
ifeq ($(shell which kubectl 2>/dev/null),)
$(error Required executable 'kubectl' not found on $$PATH)
endif
endif

ifneq ($(filter check docker-build docker-push,$(or $(MAKECMDGOALS),default)),)
ifeq ($(TELEPRESENCE_REGISTRY),)
$(error You must specify a registry with TELEPRESENCE_REGISTRY=)
endif
endif

_OS := $(shell python3 -c 'from time import time; from sys import platform; print({"linux": "LNX", "darwin": "OSX"}.get(platform))')
_TIME := $(shell date +%s)
TELEPRESENCE_VERSION := $(shell git describe --tags)$(foreach OS,$(_OS),$(foreach TIME,$(_TIME),$(VERSION_SUFFIX)))

# Attempt to get credentials cached early on while the user is still
# looking at the terminal.  They'll be required later on during the test
# suite run and the prompt is likely to be buried in test output at that
# point.
ifeq ($(filter check,$(or $(MAKECMDGOALS),default)),check)
_ = $(shell sudo echo -n)
endif

default: help
	@echo
	@echo "See                 ./docs/reference/developing.md"
	@echo "or https://telepresence.io/reference/developing.html"
.PHONY: default

#

_pytest_env  = TELEPRESENCE_REGISTRY=$(TELEPRESENCE_REGISTRY)
_pytest_env += TELEPRESENCE_VERSION=$(TELEPRESENCE_VERSION)
_pytest_env += SCOUT_DISABLE=1
check: virtualenv $(DOCKER_PUSH)  ## Run the test suite (implies 'virtualenv' and '$(DOCKER_PUSH)')
	sudo echo -n
	$(VIRTUALENV) $(_pytest_env) py.test -v --timeout=360 --timeout-method=thread $(PYTEST_ARGS)
.PHONY: check

_testbench_vars  = TELEPRESENCE_REGISTRY=$(TELEPRESENCE_REGISTRY)
_testbench_vars += TELEPRESENCE_VERSION=$(TELEPRESENCE_VERSION)
_testbench_vars += DOCKER_PUSH=''
_testbench_vars += PYTEST_ARGS='$(call escape_squotes,--tap-combined $(PYTEST_ARGS))'
testbench-check: $(DOCKER_PUSH)  ## Run the test suite in testbench (implies '$(DOCKER_PUSH)')
	+testbench CMD='make check $(call escape_squotes,$(_testbench_vars)) >&2; cat testresults.tap'
.PHONY: testbench-check

docker-build:  ## Build Docker images
	docker build --file local-docker/Dockerfile . -t $(TELEPRESENCE_REGISTRY)/telepresence-local:$(TELEPRESENCE_VERSION)
	docker build k8s-proxy -t $(TELEPRESENCE_REGISTRY)/telepresence-k8s:$(TELEPRESENCE_VERSION) --target telepresence-k8s
	docker build k8s-proxy -t $(TELEPRESENCE_REGISTRY)/telepresence-k8s-priv:$(TELEPRESENCE_VERSION) --target telepresence-k8s-priv
.PHONY: docker-build

docker-push: docker-build  ## Push Docker images to TELEPRESENCE_REGISTRY (implies 'docker-build')
	docker push $(TELEPRESENCE_REGISTRY)/telepresence-k8s:$(TELEPRESENCE_VERSION)
	docker push $(TELEPRESENCE_REGISTRY)/telepresence-k8s-priv:$(TELEPRESENCE_VERSION)
	docker push $(TELEPRESENCE_REGISTRY)/telepresence-local:$(TELEPRESENCE_VERSION)
.PHONY: docker-push

VIRTUALENV = PATH=$$PWD/virtualenv/bin:$$PATH
# Presence of __PYENV_LAUNCHER__ in pip processes we launch causes pip
# to write the wrong #! line.  Only expected to be set on OS X.
# https://bugs.python.org/issue22490
PIP = $(VIRTUALENV) env -u __PYENV_LAUNCHER__ pip
DIRFAIL = { r=$$?; rm -rf $@; exit $$r; }
virtualenv: dev-requirements.txt k8s-proxy/requirements.txt  ## Set up Python3 virtual environment for development
	rm -rf $@ || true
	virtualenv --python=python3 $@ || $(DIRFAIL)
	$(PIP) install flake8 || $(DIRFAIL)
	$(PIP) install -r dev-requirements.txt || $(DIRFAIL)
	$(PIP) install -r k8s-proxy/requirements.txt || $(DIRFAIL)
	$(PIP) install git+https://github.com/datawire/sshuttle.git@telepresence || $(DIRFAIL)
	$(PIP) install --no-use-pep517 -e . || $(DIRFAIL)

lint: virtualenv  ## Run the linters used by CI (implies 'virtualenv')
	./tools/license-check
	$(VIRTUALENV) yapf -dr telepresence packaging
	$(VIRTUALENV) flake8 --isolated local-docker k8s-proxy telepresence setup.py packaging
	$(VIRTUALENV) mypy --strict-optional telepresence local-docker/entrypoint.py packaging/*.py
	$(VIRTUALENV) mypy --ignore-missing-imports k8s-proxy
	$(VIRTUALENV) telepresence --help
.PHONY: lint

#

check-unit:  ## Like 'check', but only run unit tests
	$(MAKE) check PYTEST_ARGS='-x -k "not endtoend"'
.PHONY: check-unit

check-e2e:  ## Like 'check', but only run end-to-end tests
	$(MAKE) check PYTEST_ARGS='-x -k "endtoend"'
.PHONY: check-e2e

format: virtualenv  ## Format source code in-place
	$(VIRTUALENV) yapf -ir telepresence packaging
.PHONY: format

#

help:  ## Show this message
	@echo 'usage: make [TARGETS...] [VARIABLES...]'
	@echo
	@echo VARIABLES:
	@sed -n '/[?]=/s/^/  /p' ${MAKEFILE_LIST}
	@echo
	@echo TARGETS:
	@sed -n 's/:.*[#]#/:#/p' ${MAKEFILE_LIST} | column -t -c 2 -s ':#' | sed 's/^/  /'
.PHONY: help

# I put this as the last line in the file because it confuses Emacs
# syntax highlighting and makes the remainder of the file difficult to
# edit.
escape_squotes = $(subst ','\'',$1)
