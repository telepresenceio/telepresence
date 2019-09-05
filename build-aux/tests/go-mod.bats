#!/usr/bin/env bats

load common

setup() {
	common_setup
	echo module testlocal > go.mod
}

@test "go-mod.mk: GOTEST2TAP" {
	check_go_executable go-mod.mk GOTEST2TAP
	# TODO: Check that $GOTEST2TAP behaves correctly
}

@test "go-mod.mk: GOLANGCI_LINT" {
	check_go_executable go-mod.mk GOLANGCI_LINT
	# TODO: Check that $GOLANGCI_LINT behaves correctly
}

@test "go-mod.mk: go.lock" {
	cat >>Makefile <<-'__EOT__'
		include build-aux/go-mod.mk
		$(info outside: $(go.lock))
		all: ; @echo 'inside: $(go.lock)'
	__EOT__

	if [[ "$build_aux_unsupported_go" == true ]] || ! type go &>/dev/null; then
		make_expecting_go_error
		return 0
	fi

	make >actual
	if [[ "$(go version)" != *' go1.11'* ]]; then
		printf '%s\n' >expected \
		       "outside: " \
		       "inside: "
		diff -u expected actual
	else
		[[ $(sed -n 1p actual) == 'outside: '* ]]
		[[ $(sed -n 2p actual) == 'inside: '* ]]
		[[ $(wc -l <actual) -eq 2 ]]

		outside="$(sed -n 's/^outside: //p' actual)"
		[[ "$outside" != *' '* ]]
		[[ "$outside" == */flock ]]

		inside="$(sed -n 's/^inside: //p' actual)"
		read -ra inside_split <<<"$inside"
		[[ "${#inside_split[@]}" -eq 2 ]]
		[[ "${inside_split[0]}" == */flock ]]
		[[ "${inside_split[1]}" == "$(go env GOPATH)/pkg/mod" ]]
	fi
}

@test "go-mod.mk: build timestamps" {
	cat >>Makefile <<-'__EOT__'
		include build-aux/go-mod.mk
		all: build
	__EOT__

	cat >main.go <<-'__EOT__'
		package main

		func main() {}
	__EOT__

	mkdir sub
	cat >sub/main.go <<-'__EOT__'
		package main

		func main() {}
	__EOT__

	if [[ "$build_aux_unsupported_go" == true ]] || ! type go &>/dev/null; then
		make_expecting_go_error
		return 0
	fi

	mainexe="bin_$(go env GOHOSTOS)_$(go env GOHOSTARCH)/testlocal"
	subexe="bin_$(go env GOHOSTOS)_$(go env GOHOSTARCH)/sub"

	make

	[[ -f "$subexe" && -x "$subexe" ]]
	cp -a "$subexe" subexe.bak
	[[ ! "$subexe" -nt subexe.bak ]]

	[[ -f "$mainexe" && -x "$mainexe" ]]
	cp -a "$mainexe" mainexe.bak
	[[ ! "$mainexe" -nt mainexe.bak ]]

	sleep 2

	cat >sub/main.go <<-'__EOT__'
		package main

		import "fmt"

		func main() { fmt.Println("Hello world") }
	__EOT__

	unset CI
	make

	[[ -f "$subexe" && -x "$subexe" ]]
	[[ "$subexe" -nt subexe.bak ]]

	[[ -f "$mainexe" && -x "$mainexe" ]]
	[[ ! "$mainexe" -nt mainexe.bak ]]
}
