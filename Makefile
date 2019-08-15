# Use the versions of teleproxy and kubeapply built here, instead of
# versions pinned by build-aux.
include build-aux/prelude.mk
TELEPROXY = bin_$(GOHOSTOS)_$(GOHOSTARCH)/teleproxy
KUBEAPPLY = bin_$(GOHOSTOS)_$(GOHOSTARCH)/kubeapply

include build-aux/kubernaut-ui.mk
include build-aux/common.mk
include build-aux/go-mod.mk
include build-aux/go-version.mk
include build-aux/docker.mk
include build-aux/help.mk
include build-aux/k8s.mk
include build-aux/teleproxy.mk
include build-aux/pidfile.mk

export DOCKER_REGISTRY = $(docker.LOCALHOST):31000

build-aux/test-registry.pid: $(_kubernaut-ui.KUBECONFIG) $(KUBEAPPLY)
	$(KUBEAPPLY) -f build-aux/docker-registry.yaml
	kubectl port-forward --namespace=docker-registry deployment/registry 31000:5000 >build-aux/test-registry.log 2>&1 & echo $$! > $@
	while ! curl -i http://localhost:31000/ 2>/dev/null; do sleep 1; done
$(_kubernaut-ui.KUBECONFIG).clean: build-aux/test-registry.pid.clean
clean: build-aux/test-registry.pid.clean
	rm -f build-aux/test-registry.log

build-aux/go-test.tap: vendor build-aux/test-registry.pid

# Utility targets

release: ## Upload binaries to S3
release: release-teleproxy release-kubeapply release-kubewatch release-watt release-kubestatus
release-%: bin_$(GOOS)_$(GOARCH)/%
	aws s3 cp --acl public-read $< 's3://datawire-static-files/$*/$(VERSION)/$(GOOS)/$(GOARCH)/$*'
.PHONY: release release-%

consul.local:
	docker run --rm -it -p8500:8500 --name=consul consul:1.4.3
consul.attach:
	docker exec -it consul /bin/sh
.PHONY: consul.local consul.attach
