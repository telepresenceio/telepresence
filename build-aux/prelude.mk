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

# This file deals with baseline 'Makefile' utilities, without doing
# anything specific to Telepresence.

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

# Add a rule to generate `make help` output from comments in the
# Makefiles.
.PHONY: help
help:  ## (ZSupport) Show this message
	@echo 'Usage: [VARIABLE=VALUE...] $(MAKE) [TARGETS...]'
	@echo
	@echo VARIABLES:
	@{ $(foreach varname,$(shell sed -n '/[?]=/{ s/[ ?].*//; s/^/  /; p; }' $(sort $(abspath $(MAKEFILE_LIST)))),printf '%s = %s\n' '$(varname)' '$($(varname))';) } | column -t | sed 's/^/  /'
	@echo
	@echo TARGETS:
	@sed -En 's/^([^:]*):[^#]*## *(\([^)]*\))? */\2	\1	/p' $(sort $(abspath $(MAKEFILE_LIST))) | sed 's/^	/($(or $(NAME),this project))&/' | column -t -s '	' | sed 's/^/  /' | sort
	@echo
	@echo "See DEVELOPING.md for more information"
