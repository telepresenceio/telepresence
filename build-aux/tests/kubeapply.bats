#!/usr/bin/env bats

load common

@test "kubeapply.mk: KUBEAPPLY" {
	check_go_executable kubeapply.mk KUBEAPPLY
	# TODO: Check that $KUBEAPPLY behaves correctly
}
