include build-aux/kubernaut-ui.mk
include build-aux/common.mk
include build-aux/go-mod.mk
include build-aux/go-version.mk
include build-aux/docker.mk
include build-aux/help.mk
include build-aux/k8s.mk
include build-aux/teleproxy.mk

# Utility targets

release: ## Upload binaries to S3
release: release-teleproxy release-kubeapply release-kubewatch release-watt
release-%: bin_$(GOOS)_$(GOARCH)/%
	aws s3 cp --acl public-read $< 's3://datawire-static-files/$*/$(VERSION)/$(GOOS)/$(GOARCH)/$*'
.PHONY: release release-%

consul.local:
	docker run --rm -it -p8500:8500 --name=consul consul:1.4.3
consul.attach:
	docker exec -it consul /bin/sh
.PHONY: consul.local consul.attach
