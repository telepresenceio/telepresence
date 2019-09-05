# Copyright 2019 Datawire. All rights reserved.
#
# Makefile snippet for building, tagging, and pushing Docker images.
#
## Eager inputs ##
#  - Variables     : docker.tag.$(GROUP)     # define %.docker.tag.$(GROUP) and %.docker.push.$(GROUP) targets
## Lazy inputs ##
#  (none)
## Outputs ##
#
#  - Executable    : WRITE_DOCKERTAGFILE ?= $(CURDIR)/build-aux/bin/write-dockertagfile
#
#  - Variable      : HAVE_DOCKER             # non-empty if true, empty if false
#  - Variable      : docker.LOCALHOST        # "host.docker.internal" on Docker for Desktop, "localhost" on Docker CE
#
#  - Target        : %.docker: %/Dockerfile               # build image (untagged)
#  - Target        : %.docker.tag.$(GROUP)                # tag image as $(docker.tag.$(GROUP))
#  - Target        : %.docker.push.$(GROUP)               # push tag(s) $(docker.tag.$(GROUP))
#  - .PHONY Target : %.docker.clean                       # remove image and tags
#
## common.mk targets ##
#  (none)
#
# ## Local docker build ##
#
#    To use this Makefile snippet naively, `Dockerfile`s must be in
#    sub-directories; it doesn't support out-of-the-box having a
#    `Dockerfile` in the root.  If you would like to have
#    `Dockerfile`s out of the root (or with other names, like
#    `Dockerfile.base-envoy`), then you must supply your own
#    `%.docker` target that
#      1. Calls `docker build --iidfile=TEMPFILE ...`
#      2. Calls `$(MOVE_IFCHANGED) TEMPFILE $@`
#
#    You can build a Docker image by depending on `SOMEPATH.docker`.
#    If you provide a custom `%.docker` rule, then of course exactly
#    what that builds will be different, but with the default built-in
#    rule: depending on `SOMEPATH.docker` will build
#    `SOMEPATH/Dockefile`.  This will build the image, but NOT tag it
#    (see below for tagging).
#
#    You can untag and remove an image by having your `clean` target
#    depend on `SOMEPATH.docker.clean`.
#
#    With the default built-in rule:
#
#     - If you need something to be done before the `docker build`,
#       make it a dependency of `SOMEPATH.docker`.
#
#     - If you need something (`FILE`) to be included in the build
#       context, copy it to `SOMEPATH/` by having
#       `SOMEPATH.docker` depend on `SOMEPATH/FILE`.
#
# ## Working with those untagged images ##
#
#     - Tagging: You can tag an image after being built by depending
#       on `SOMEPATH.docker.tag.GROUP`, where you've set up GROUP by
#       writing
#
#           docker.tag.GROUP = EXPR
#
#       _before_ including docker.mk, where GROUP is the suffix of the
#       target that you'd like to depend on in your Makefile, and EXPR
#       is a Makefile expression that evaluates to 1 or more tag
#       names; it is evaluated in the context of
#       `SOMEPATH.docker.tag.GROUP`; specifically:
#        * `$*` is set to SOMEPATH
#        * `$<` is set to a file containing the image ID
#
#       Additionally, you can override the EXPR on a per-image basis
#       by overriding the `docker.tag.GROUP` variable on a per-target
#       basis:
#
#           SOMEPATH.docker.tag.GROUP: docker.tag.GROUP = EXPR
#
#        > For example:
#        >
#        >     docker.tag.release    = quay.io/datawire/ambassador_pro:$(notdir $*)-$(VERSION)
#        >     docker.tag.buildcache = quay.io/datawire/ambassador_pro-buildcache:$(notdir $*)-$(VERSION)
#        >     include build-aux/docker.mk
#        >     # The above will cause docker.mk to define targets:
#        >     #  - %.docker.tag.release
#        >     #  - %.docker.push.release
#        >     #  - %.docker.tag.buildcache
#        >     #  - %.docker.push.buildcache
#        >
#        >     # Override the release name a specific image.
#        >     # Release ambassador-withlicense/ambassador.docker
#        >     #  - based on the above    : quay.io/datawire/ambassador_pro:ambassador-$(VERSION)
#        >     #  - after being overridden: quay.io/datawire/ambassador_pro:amb-core-$(VERSION)
#        >     ambassador-withlicense/ambassador.docker.tag.release: docker.tag.release = quay.io/datawire/ambassador_pro:amb-core-$(VERSION)
#
#     - Pushing a tag: You can push tags that have been created with
#       `SOMEPATH.docker.tag.GROUP` (see above) by depending on
#       `SOMEPATH.docker.push.GROUP`.
#
#          > For example:
#          >   The Ambassador Pro images:
#          >    - get built from: `docker/$(NAME)/Dockerfile`
#          >    - get pushed as : `quay.io/datawire/ambassador_pro:$(NAME)-$(VERSION)`
#          >
#          >   We accomplish this by saying:
#          >
#          >      docker.tag.release = quay.io/datawire/ambassador_pro:$(notdir $*)-$(VERSION)
#          >
#          >   and having our `build`   target depend on `NAME.docker.tag.release` (for each NAME)
#          >   and having our `release` target depend on `NAME.docker.push.release` (for each NAME)
#
#     - Clean up: You can untag (if there are any tags) and remove an
#       image by having your `clean` target depend on
#       `SOMEPATH.docker.clean`.  Because docker.mk does not have a
#       listing of all the images you may ask it to build, these are
#       NOT automatically added to the common.mk 'clean' target, and
#       you MUST do that yourself.
#
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
_docker.mk := $(lastword $(MAKEFILE_LIST))
include $(dir $(_docker.mk))prelude.mk

#
# Inputs

_docker.tag.groups = $(patsubst docker.tag.%,%,$(filter docker.tag.%,$(.VARIABLES)))
# clean.groups is separate from tag.groups as a special-case for docker-cluster.mk
_docker.clean.groups += $(_docker.tag.groups)

#
# Executables

WRITE_DOCKERTAGFILE ?= $(build-aux.bindir)/write-dockertagfile

#
# Output variables

HAVE_DOCKER      = $(call lazyonce,HAVE_DOCKER,$(shell which docker 2>/dev/null))
docker.LOCALHOST = $(if $(filter darwin,$(GOHOSTOS)),host.docker.internal,localhost)

#
# Output targets

# file contents:
#   line 1: image ID
%.docker: %/Dockerfile $(MOVE_IFCHANGED) FORCE
# Try with --pull, fall back to without --pull
	docker build --iidfile=$(@D)/.tmp.$(@F).tmp --pull $* || docker build --iidfile=$(@D)/.tmp.$(@F).tmp $*
	$(MOVE_IFCHANGED) $(@D)/.tmp.$(@F).tmp $@

%.docker.clean: $(addprefix %.docker.clean.,$(_docker.clean.groups))
	if [ -e $*.docker ]; then docker image rm "$$(cat $*.docker)" || true; fi
	rm -f $*.docker
.PHONY: %.docker.clean

# Evaluate _docker.tag.rule with _docker.tag.group=TAG_GROUPNAME for
# each docker.tag.TAG_GROUPNAME variable.
#
# Add a set of %.docker.tag.TAG_GROUPNAME and
# %.docker.push.TAG_GROUPNAME targets that tag and push the docker image.
define _docker.tag.rule
  # file contents:
  #   line 1: image ID
  #   line 2: tag 1
  #   line 3: tag 2
  #   ...
  %.docker.tag.$(_docker.tag.group): %.docker $$(WRITE_DOCKERTAGFILE) FORCE
  # The 'foreach' is to handle newlines as normal whitespace
	printf '%s\n' $$$$(cat $$<) $$(foreach v,$$(docker.tag.$(_docker.tag.group)),$$v) | $$(WRITE_DOCKERTAGFILE) $$@

  # file contents:
  #   line 1: image ID
  #   line 2: tag 1
  #   line 3: tag 2
  #   ...
  %.docker.push.$(_docker.tag.group): %.docker.tag.$(_docker.tag.group)
	sed 1d $$< | xargs -n1 docker push
	cat $$< > $$@

  %.docker.clean.$(_docker.tag.group):
	if [ -e $$*.docker.tag.$(_docker.tag.group) ]; then docker image rm $$$$(cat $$*.docker.tag.$(_docker.tag.group)) || true; fi
	rm -f $$*.docker.tag.$(_docker.tag.group) $$*.docker.push.$(_docker.tag.group)
  .PHONY: %.docker.clean.$(_docker.tag.group)
endef
$(foreach _docker.tag.group,$(_docker.tag.groups),$(eval $(_docker.tag.rule)))

endif
