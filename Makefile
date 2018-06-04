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

.PHONY: default version registry format lint unit e2e help

VERSION=$(shell python3 setup.py --version)${TELEPRESENCE_VER_SUFFIX}
TELEPRESENCE_REGISTRY?=${USER}
SHELL:=/bin/bash

default: help
	@echo
	@echo "See https://telepresence.io/reference/developing.html"

version:
	@echo $(VERSION)

registry:
	@echo $(TELEPRESENCE_REGISTRY)

## Setup dependencies ##

virtualenv:  ## Set up Python3 virtual environment for development
	./build --manage-virtualenv --no-tests

## Development ##

format:  ## Format source code in-place
format: virtualenv
	virtualenv/bin/yapf -ir telepresence build

lint:  ## Run the linters used by CI
	./build --lint --no-tests

unit:  ## Run the unit tests
	./build --registry $(TELEPRESENCE_REGISTRY) -- -x -k "not endtoend"

e2e:  ## Run the end-to-end tests
	./build --registry $(TELEPRESENCE_REGISTRY) -- -x -k "endtoend"

## Help - https://gist.github.com/prwhite/8168133#gistcomment-1737630

help:  ## Show this message
	@echo 'usage: make [target] ...'
	@echo
	@egrep '^(.+)\:  ##\ (.+)' ${MAKEFILE_LIST} | column -t -c 2 -s ':#'
