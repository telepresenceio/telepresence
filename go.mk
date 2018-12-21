
GOPATH=$(CURDIR)/.go-workspace
GOBIN=$(CURDIR)
GO = GOPATH=$(GOPATH) GOBIN=$(GOBIN) go

build: $(bins)
.PHONY: build

$(bins): %: get FORCE
	$(GO) install $(pkg)/cmd/$@

get:
	$(GO) get -t -d ./...
.PHONY: get

.SECONDARY:
# The only reason .DELETE_ON_ERROR is off by default is for historical
# compatibility.
.DELETE_ON_ERROR:
# .NOTPARALLEL is important, as having multiple `go install`s going at
# once can corrupt `$(GOPATH)/pkg`.  Setting .NOTPARALLEL is simpler
# than mucking with multi-target pattern rules.
.NOTPARALLEL:
# The $(bins) aren't .PHONY--they're real files that will exist, but
# we should try to update them every run, and let `go` decide if
# they're up-to-date or not, rather than trying to teach Make to do
# it.  So instead, have them depend on a .PHONY target so that they'll
# always be considered out-of-date.
.PHONY: FORCE
