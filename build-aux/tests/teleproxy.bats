#!/usr/bin/env bats

load common

@test "teleproxy.mk: TELEPROXY" {
	check_go_executable teleproxy.mk TELEPROXY
	# TODO: Check that $TELEPROXY behaves correctly
}
