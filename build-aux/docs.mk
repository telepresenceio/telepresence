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

subtree-preflight:
	@if ! grep -q -e 'has_been_added' $$(PATH=$$(git --exec-path):$$PATH which git-subtree 2>/dev/null) /dev/null; then \
	    printf '$(RED)Please upgrade your git-subtree:$(END)\n'; \
	    printf '$(BLD)  sudo curl -fL https://raw.githubusercontent.com/LukeShu/git/lukeshu/next/2021-05-15/contrib/subtree/git-subtree.sh -o $$(git --exec-path)/git-subtree && sudo chmod 755 $$(git --exec-path)/git-subtree$(END)\n'; \
	    false; \
	else \
	    printf '$(GRN)git-subtree OK$(END)\n'; \
	fi
	git gc
.PHONY: subtree-preflight

PULL_PREFIX ?=
PUSH_PREFIX ?= $(USER)/from-telepresence.io-$(shell date +%Y-%m-%d)/

dir2branch = $(patsubst docs/%,release/%,$(subst pre-release,v2,$1))

pull-docs: ## Update ./docs from https://github.com/telepresenceio/docs
pull-docs: subtree-preflight
	$(foreach subdir,$(shell find docs -mindepth 1 -maxdepth 1 -type d|sort -V),\
          git subtree pull --squash --prefix=$(subdir) https://github.com/telepresenceio/docs $(PULL_PREFIX)$(call dir2branch,$(subdir))$(nl))
.PHONY: pull-docs

PUSH_BRANCH ?= $(USER)/from-telepresence.io-$(shell date +%Y-%m-%d)
push-docs: ## Publish ./docs to https://github.com/telepresenceio/docs
push-docs: subtree-preflight
	@PS4=; set -x; { \
	  git remote add --no-tags remote-docs https://github.com/telepresenceio/docs && \
	  git remote set-url --push remote-docs git@github.com:telepresenceio/docs && \
	:; } || true
	git fetch --prune remote-docs
	$(foreach subdir,$(shell find docs -mindepth 1 -maxdepth 1 -type d|sort -V),\
          git subtree push --rejoin --squash --prefix=$(subdir) remote-docs $(PUSH_PREFIX)$(call dir2branch,$(subdir))$(nl))
.PHONY: push-docs
