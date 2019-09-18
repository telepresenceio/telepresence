# Use the versions of teleproxy and kubeapply built here, instead of
# versions pinned by build-aux.
include build-aux/prelude.mk
TELEPROXY = bin_$(GOHOSTOS)_$(GOHOSTARCH)/teleproxy
KUBEAPPLY = bin_$(GOHOSTOS)_$(GOHOSTARCH)/kubeapply

include build-aux/common.mk
include build-aux/go-mod.mk
include build-aux/go-version.mk
include build-aux/help.mk

.DEFAULT_GOAL = help

build-aux/go-test.tap: vendor

# Edge Control tests require calling out to the edgectl binary
build-aux/go-test.tap: bin_$(GOHOSTOS)_$(GOHOSTARCH)/edgectl

# Utility targets

release: ## Upload binaries to S3
release: release-teleproxy release-kubeapply release-kubewatch release-watt release-k3sctl release-kubestatus release-edgectl
release-%: bin_$(GOOS)_$(GOARCH)/%
	aws s3 cp --acl public-read $< 's3://datawire-static-files/$*/$(VERSION)/$(GOOS)/$(GOARCH)/$*'
.PHONY: release release-%

consul.local:
	docker run --rm -it -p8500:8500 --name=consul consul:1.4.3
consul.attach:
	docker exec -it consul /bin/sh
.PHONY: consul.local consul.attach
