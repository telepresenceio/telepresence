# We need to pass special `-exec …` flags to to `go test` for certain
# packages, so disable go-mod.mk's built-in go-test, and we'll define
# our own below
go.DISABLE_GO_TEST = y

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

test-suite.tap: .go-test.tap
check: test-cluster
go-test: test-cluster
	$(MAKE) .go-test.tap.summary
.go-test.tap: .go-test-nat.tap .go-test-teleproxy.tap .go-test-other.tap
	@./build-aux/tap-driver cat $^ > $@
# .tap-file                    [----firewall.lock----] [----cluster.lock----]               [---------------extra flags--------------] [----------------------------------------packages-----------------------------------------]
.go-test-nat.tap      : FORCE; @$(FLOCK) .firewall.lock                        go test -json -exec sudo                                 $(go.module)/internal/pkg/nat                                                               | GO111MODULE=off go run build-aux/gotest2tap.go | tee $@ | build-aux/tap-driver stream -n $@
.go-test-teleproxy.tap: FORCE; @$(FLOCK) .firewall.lock $(FLOCK) .cluster.lock go test -json -exec "sudo env KUBECONFIG=$${KUBECONFIG}" $(go.module)/cmd/teleproxy                                                                  | GO111MODULE=off go run build-aux/gotest2tap.go | tee $@ | build-aux/tap-driver stream -n $@
.go-test-other.tap    : FORCE; @                        $(FLOCK) .cluster.lock go test -json                                            $$(go list ./... | grep -vF -e $(go.module)/internal/pkg/nat -e $(go.module)/cmd/teleproxy) | GO111MODULE=off go run build-aux/gotest2tap.go | tee $@ | build-aux/tap-driver stream -n $@

ifneq ($(shell which docker 2>/dev/null),)
.docker.tap: docker/teleproxy-check.docker
	@$(if $(filter linux,$(GOOS)),$(FLOCK) .firewall.lock) docker run --rm --cap-add=NET_ADMIN "$$(sed -n 3p $<)" go test -json -exec sudo $(go.module)/internal/pkg/nat | GO111MODULE=off go run build-aux/gotest2tap.go | tee $@ | build-aux/tap-driver stream -n $@
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
.docker.tap: .go-test-nat.tap
	@sed -En -e '/^TAP version/p' -e 's/^(not )?ok([^#]*)(#.*)?/ok\2 # SKIP/p' -e '/^[0-9]+\.\.[0-9]+$$/p' $< | tee $@ | build-aux/tap-driver stream -n $@
endif
test-suite.tap: .docker.tap

clean:
	$(FLOCK) .firewall.lock rm .firewall.lock
	$(FLOCK) .cluster.lock rm .cluster.lock
	rm -f -- .*.tap

# Utility targets

run: build
	bin_$(GOOS)_$(GOARCH)/teleproxy
.PHONY: run

release: ## Upload binaries to S3
release: release-teleproxy release-kubeapply release-kubewatch
release-%: bin_$(GOOS)_$(GOARCH)/%
	aws s3 cp --acl public-read $< 's3://datawire-static-files/$*/$(VERSION)/$(GOOS)/$(GOARCH)/$*'
.PHONY: release release-%
