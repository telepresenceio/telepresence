include build-aux/common.mk
include build-aux/go-mod.mk
include build-aux/go-version.mk
include build-aux/flock.mk
include build-aux/docker.mk
include build-aux/help.mk

.DEFAULT_GOAL = help

export PATH:=$(CURDIR)/bin_$(GOOS)_$(GOARCH):$(PATH)

test-cluster: $(KUBECONFIG) bin_$(GOOS)_$(GOARCH)/kubeapply
	bin_$(GOOS)_$(GOARCH)/kubeapply -f k8s
.PHONY: test-cluster

# We need to pass special `-exec …` flags to to `go test` for certain
# packages, so disable go.mk's built-in go-test, and define our own.
go.DISABLE_GO_TEST = y
go-test-nat: go-get
	$(FLOCK) .firewall.lock go test -v -exec sudo $(go.module)/internal/pkg/nat
go-test-teleproxy: go-get test-cluster
	$(FLOCK) .firewall.lock $(FLOCK) .cluster.lock go test -v -exec "sudo env KUBECONFIG=$${KUBECONFIG}" $(go.module)/cmd/teleproxy
go-test-other: go-get test-cluster
	$(FLOCK) .cluster.lock go test -v $$(go list ./... | grep -vF -e $(go.module)/internal/pkg/nat -e $(go.module)/cmd/teleproxy)
.PHONY: go-test-nat go-test-teleproxy go-test-other
go-test: go-test-nat go-test-teleproxy go-test-other

check-docker:
ifneq ($(shell which docker 2>/dev/null),)
check-docker: docker/teleproxy-check.docker
check-docker:
	$(if $(filter linux,$(GOOS)),$(FLOCK) .firewall.lock) docker run --rm --cap-add=NET_ADMIN $(docker.LOCALHOST):31000/teleproxy-check:$(VERSION) go-test-nat
docker/teleproxy-check.docker: docker/teleproxy-check/teleproxy.tar
docker/teleproxy-check/teleproxy.tar: go-get
	rm -f $@
	{ git ls-files; git ls-files --others --exclude-standard; } | while IFS='' read -r file; do \
	    if [ -e "$$file" -o -L "$$file" ]; then \
	        mkdir -p "$$(dirname "$(@D)/.tmp/teleproxy/$$file")"; \
	        cp "$$file" "$(@D)/.tmp/teleproxy/$$file"; \
	    fi; \
	done; \
	cd $(@D)/.tmp/teleproxy && go mod vendor
	tar -c -f $@ -C $(@D)/.tmp/teleproxy .
	rm -rf $(@D)/.tmp
else
	@echo "SKIPPING DOCKER TESTS"
endif
.PHONY: check-docker
check: check-docker

clean:
	$(FLOCK) .firewall.lock rm .firewall.lock
	$(FLOCK) .cluster.lock rm .cluster.lock

# Utility targets

run: build
	bin_$(GOOS)_$(GOARCH)/teleproxy
.PHONY: run
