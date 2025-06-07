# Avoid potential confusion about what shell to use
SHELL := /bin/bash

# Delete implicit rules not used here (clutters debug output).
MAKEFLAGS += --no-builtin-rules

# Turn off .INTERMEDIATE file removal by marking all files as
# .SECONDARY.  .INTERMEDIATE file removal is a space-saving hack from
# a time when drives were small; on modern computers with plenty of
# storage, it causes nothing but headaches.
#
# https://news.ycombinator.com/item?id=16486331
.SECONDARY:

# If a recipe errors, remove the target it was building.  This
# prevents outdated/incomplete results of failed runs from tainting
# future runs.  The only reason .DELETE_ON_ERROR is off by default is
# for historical compatibility.
#
# If for some reason this behavior is not desired for a specific
# target, mark that target as .PRECIOUS.
.DELETE_ON_ERROR:

PLUGIN_ARCH ?= $(shell go env GOARCH)
PLUGIN_ARCH := $(PLUGIN_ARCH)
PLUGIN_REGISTRY ?= ghcr.io/telepresenceio
PLUGIN_NAME = teleroute
PLUGIN_FQN = $(PLUGIN_REGISTRY)/$(PLUGIN_NAME)
PLUGIN_DEV_IMAGE = $(PLUGIN_FQN):$(PLUGIN_ARCH)

BUILD_DIR=build-output

export DOCKER_BUILDKIT := 1

.PHONY: clean
clean:
	rm -rf $(BUILD_DIR) rpc

rpc/teleroute/.rsync-stamp: ../../rpc/go.mod ../../rpc/go.sum $(wildcard ../../rpc/teleroute/*.go)
	mkdir -p rpc/teleroute
	rsync -au --delete ../../rpc/go.mod rpc/
	rsync -au --delete ../../rpc/go.sum rpc/
	rsync -au --delete ../../rpc/teleroute rpc/
	touch rpc/teleroute/.rsync-stamp

.PHONY: rootfs
rootfs: rpc/teleroute/.rsync-stamp
	docker buildx inspect |grep -q /$(PLUGIN_ARCH) || \
	docker run --rm --privileged tonistiigi/binfmt --install all
	rm -rf $(BUILD_DIR)
	docker buildx build --platform linux/$(PLUGIN_ARCH) --output $(BUILD_DIR)/rootfs .
	cp config.json $(BUILD_DIR)

# Enable is to support faster dev-loop (retries without pushing)
.PHONY: enable
enable: rootfs
	docker plugin rm --force $(PLUGIN_DEV_IMAGE) 2>/dev/null || true
	docker plugin create $(PLUGIN_DEV_IMAGE) $(BUILD_DIR)
	docker plugin enable $(PLUGIN_DEV_IMAGE)

.PHONY: debug
debug: rootfs
	docker plugin rm --force $(PLUGIN_DEV_IMAGE)-debug 2>/dev/null || true
	docker plugin create $(PLUGIN_DEV_IMAGE)-debug $(BUILD_DIR)
	docker plugin set $(PLUGIN_DEV_IMAGE)-debug DEBUG=true
	docker plugin enable $(PLUGIN_DEV_IMAGE)-debug

# Recreate the plugin from the rootfs with some tag. This target is called
# repeatedly in order to give the plugin different tags (because plugins cannot
# be tagged like images).
$(BUILD_DIR)/tag-%.ts:
	docker plugin rm --force $(PLUGIN_FQN):$* 2>/dev/null || true
	docker plugin create $(PLUGIN_FQN):$* $(BUILD_DIR)
	docker plugin push $(PLUGIN_FQN):$*
	docker plugin rm --force $(PLUGIN_FQN):$* 2>/dev/null || true
	touch $(BUILD_DIR)/tag-$*.ts

gaPattern = ^[0-9]+.[0-9]+.[0-9]+$$

# For amd64 we push the tags "amd64-SEMVER" and "SEMVER", and if it is a release, also "amd64" and "latest".
# For arm64 we push the tag "arm64-SEMVER", and if it is a release also "arm64".
.PHONY: push
ifeq ($(PLUGIN_ARCH), amd64)
push:  clean rootfs $(BUILD_DIR)/tag-$(PLUGIN_ARCH)-$(PLUGIN_VERSION).ts $(BUILD_DIR)/tag-$(PLUGIN_VERSION).ts
else
push:  clean rootfs $(BUILD_DIR)/tag-$(PLUGIN_ARCH)-$(PLUGIN_VERSION).ts
endif
	if [[ $(PLUGIN_VERSION) =~ $(gaPattern) ]]; \
	then \
	  make push-latest; \
	fi

.PHONY: push-latest
ifeq ($(PLUGIN_ARCH), amd64)
push-latest:  $(BUILD_DIR)/tag-$(PLUGIN_ARCH).ts $(BUILD_DIR)/tag-latest.ts
else
push-latest:  $(BUILD_DIR)/tag-$(PLUGIN_ARCH).ts
endif
