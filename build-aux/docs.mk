# Copyright 2020-2021 Datawire.  All rights reserved.
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

# This file deals with syncing docs between the code and telepresenceio/docs repo.
# This is the same file as https://github.com/telepresenceio/telepresence.io/blob/master/Makefile
# and should probably stay that way.

define nl


endef

check-subrepo-installed:
	@if ! which git-subrepo 2>/dev/null; then \
		printf 'git-subrepo must be installed:\n'; \
		printf 'https://github.com/ingydotnet/git-subrepo\n'; \
		false; \
	fi
.PHONY: check-subrepo-installed

PUSH_PREFIX ?= $(USER)/from-telepresence.io-$(shell date +%Y-%m-%d)/

dir2branch = $(patsubst docs/%,release/%,$(subst pre-release,v2,$1))

pull-docs-subrepos: ## Update ./docs from https://github.com/telepresenceio/docs
pull-docs-subrepos: check-subrepo-installed
	$(foreach subdir,$(shell find docs -mindepth 1 -maxdepth 1 -type d|sort -V),\
          git subrepo -v pull $(subdir) -b $(PULL_PREFIX)$(call dir2branch,$(subdir))$(nl))
.PHONY: pull-docs

PUSH_BRANCH ?= $(USER)/from-telepresence.io-$(shell date +%Y-%m-%d)
push-docs-subrepos: ## Publish ./docs to https://github.com/telepresenceio/docs
push-docs-subrepos:
	$(foreach subdir,$(shell find docs -mindepth 1 -maxdepth 1 -type d|sort -V),\
          git subrepo -v push $(subdir) -b $(PUSH_PREFIX)$(call dir2branch,$(subdir))$(nl))
.PHONY: push-docs
