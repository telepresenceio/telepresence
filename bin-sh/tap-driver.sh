#!/usr/bin/env bash
# Copyright 2019 Datawire. All rights reserved.
#
# Dependencies:
#  - Bash (tested with 3.2.57 and newer)
#  - /usr/bin/env (for the shebang)
#  - uname (for deciding whether libc's regcomp(3) uses `\b` or `[[:>:]]`)
#  - cat
#  - tput (optional)
# Other than that, this *only* uses Bash builtins, for better
# portability.
#
# Notes:
#    This is 500 lines of pure Bash.  I (LukeShu) think it's clean and
#    editable, but I'm more comfortable with Bash than most.  It's
#    conceptually very similar to Automake's `tap-driver.sh`, which is
#    about the same length, but a little bit of POSIX shell, and a
#    whole lot of AWK (and I think isn't very understandable).  The
#    bulk of mine is a TAP parser, strongly based on the one I wrote
#    in Python for [testbench-matrix][].
#     - I considered writing it in Python, but decided against it
#       because I didn't want to expand the build-footprint of
#       build-aux, which currently does not depend on Python.
#     - I considered writing it in Perl, because it's part of the base
#       install just about everywhere, but decided against it because
#       I figured everyone else at Datawire would hate that even more
#       than Bash.
#     - I don't know why I didn't write it in Go.  Writing it in Go
#       would have made a lot of sense.  Rewriting it in Go is an
#       option on the table that I don't oppose.
#
# [testbench-matrix]: https://github.com/datawire/testbench/tree/master/testbench/tap/matrix
set -euE -o pipefail

arg0=${0##*/}
readonly arg0

# regex end-of-word boundry
if [[ "$(uname)" == Darwin ]]; then
	re_b='[[:>:]]'
else
	re_b='\b'
fi
readonly re_b

usage() {
	printf 'Usage: %s stream [-n NAME] < TAP\n' "$arg0"
	printf '   or: %s cat FILE1.tap FILE2.tap...\n' "$arg0"
	printf '   or: %s summarize FILE.tap\n' "$arg0"
	printf '   or: %s help\n' "$arg0"
}

errusage() {
	if (( $# > 0)); then
		printf '%s: %s\n' "$arg0" "$(printf "$@")" >&2
	fi
	printf "Try '%s help' for more information.\n" "$arg0" >&2
	exit 2
}

panic() {
	printf '%s: %s\n' "$arg0" "$(printf 'The programmer who wrote %s made a mistake' "$arg0")" >&2
	exit 255
}

# Usage: in_array "$needle" "${haystack[@]}"
in_array() {
	local straw
	for straw in "${@:2}"; do
		if [[ "$1" = "$straw" ]]; then
			return 0
		fi
	done
	return 1
}

trim() {
	local str=$1
	while [[ $str == ' '* ]]; do
		str=${str# }
	done
	while [[ $str == *' ' ]]; do
		str=${str% }
	done
	printf '%s' "$str"
}

main() {
	if test -t 1 && tput setaf 0 &>/dev/null; then
		ALL_OFF=$(tput sgr0)
		GREEN=$(tput setaf 2)
		BLUE=$(tput setaf 4)
		RED=$(tput setaf 1)
		PURPLE=$(tput setaf 5)
	else
		ALL_OFF=
		GREEN=
		BLUE=
		RED=
		PURPLE=
	fi
	readonly ALL_OFF GREEN BLUE RED PURPLE

	if (( $# == 0 )); then
		errusage 'Must specify a command'
	fi
	case "$1" in
		stream) cmd_stream "${@:2}";;
		cat) cmd_cat "${@:2}";;
		summarize) cmd_summarize "${@:2}";;
		help) usage;;
		*) errusage 'Unknown command: %s' "$1";;
	esac
}

# Usage:
#   tap_handle_testline() { local status=$1 number=$2 description=$3 comment=$4 yaml=$5 directives=("${@:6}"); … }
#   tap_handle_comment() { local comment=$1; … }
#   tap_handle_error() { local error=$1; … }
#   tap_parse < TAP
tap_parse() {
	local tap_line tap_next_line tap_lineno=0
	local _tap_readline_next _tap_readline_status= _tap_readline_havenext=false
	tap_readline() {
		tap_lineno=$((tap_lineno+1))
		if $_tap_readline_havenext; then
			tap_line=$_tap_readline_next
			_tap_readline_havenext=false
			return "$_tap_readline_status"
		else
			IFS='' read -r tap_line
		fi
	}
	tap_peekline() {
		if ! $_tap_readline_havenext; then
			_tap_readline_status=0
			IFS='' read -r _tap_readline_next || _tap_readline_status=$?
		fi
		if (( _tap_readline_status == 0 )); then
			tap_next_line=$_tap_readline_next
			_tap_readline_havenext=true
		fi
		return $_tap_readline_status
	}
	tap_flush() {
		_tap_readline_havenext=false
		cat >/dev/null
	}

	tap_error() {
		tap_handle_error "Invalid TAP: $(printf "$@")"
		tap_flush
	}

	local tap_version=12
	if tap_peekline && [[ "$tap_next_line" = 'TAP version '* ]]; then
		tap_version=${tap_next_line#TAP version }
		if ! [[ $tap_version =~ ^[[:digit:]]+$ ]]; then
			tap_error 'Not an integer version: %s' "$tap_version"
			return
		fi
		if (( tap_version < 13 )); then
			tap_error 'It is illegal to specify a TAP version < 13, got: %s' "$tap_version"
			return
		fi
	fi
	case "$tap_version" in
		12) tap12_parse;;
		13) tap13_parse;;
		*) tap_error "I don't know how to parse TAP version %s" "$tap_version";;
	esac
}

tap12_parse() {
	tap_error() {
		tap_handle_error "Invalid TAP12: $(printf "$@")"
		tap_flush
	}

	local tap_test_re='^(ok|not ok)'"$re_b"'[[:space:]]*([0-9]+)?([^#]*)(#.*)?'
	#                   1                               2        3      4
	#
	# 1: status (required)
	# 2: number (recommended)
	# 3: description (recommended)
	# 4: comment (when necessary)

	local tap_at_end=false
	local tap_plan=
	local tap_prev_test_number=0 tap_test_count=0 # Not the same, tests can be out-of-order
	local tap_test_status tap_test_number tap_test_description tap_test_comment tap_test_yaml tap_test_directives
	while tap_readline; do
		if [[ "$tap_line" = '#'* ]]; then
			tap_handle_comment "$tap_line"
		elif $tap_at_end; then
			tap_error 'Cannot have more output after trailing test plan'
		elif [[ "$tap_line" = 'TAP version ' ]]; then
			tap_error 'Cannot specify a version: %q' "$tap_line"
		elif [[ "$tap_line" = '1..'* ]]; then
			if [[ -n "$tap_plan" ]]; then
				tap_error 'Test plan can only be given once'
				continue
			fi
			tap_plan=${tap_line#1..}
			if ! [[ $tap_plan =~ ^[[:digit:]]+$ ]]; then
				tap_error 'Not an integer number of tests: %s' "$tap_plan"
				continue
			fi
			if (( tap_test_count > 0 )); then
				tap_at_end=true
			fi
		elif [[ "$tap_line" =~ $tap_test_re ]]; then
			tap_test_status=${BASH_REMATCH[1]}
			tap_test_number=${BASH_REMATCH[2]:-$((tap_prev_test_number+1))}
			tap_test_description=$(trim "${BASH_REMATCH[3]:-}")
			tap_test_comment=${BASH_REMATCH[4]:-}

			tap_test_yaml=''

			# As a hack for Bash <4.4 considering empty arrays to be unset,
			# always include an empty directive.
			tap_test_directives=('')
			if (shopt -s nocasematch; [[ "$tap_test_comment" =~ ^#\ TODO( .*)?$ ]]); then
				tap_test_directives+=('TODO')
			fi
			if (shopt -s nocasematch; [[ "$tap_test_comment" = '# SKIP'* ]]); then
				tap_test_directives+=('SKIP')
			fi

			tap_handle_testline \
				"$tap_test_status" \
				"$tap_test_number" \
				"$tap_test_description" \
				"$tap_test_comment" \
				"$tap_test_yaml" \
				"${tap_test_directives[@]}"

			tap_prev_test_number=$tap_test_number
			tap_test_count=$((tap_test_count+1))
		elif [[ "$tap_line" = 'Bail out!'* ]]; then
			tap_handle_error "$tap_line"
			# Continue parsing, it will result in better error messages/summaries
		else
			# Spec says to silently ignore unknown lines
			true
		fi
	done

	if [[ -n "$tap_plan" ]]; then
		if (( tap_test_count != tap_plan )); then
			tap_error 'Expected %d tests, got %d' "$tap_plan" "$tap_test_count"
		fi
	else
		if (( tap_test_count == 0 )); then
			# Any old file that isn't TAP will parse as "valid" TAP 12
			# with 0 tests, since all headers are optional, and unknown
			# lines are ignored.  So, as a special case to achieve sane
			# behavior (but in violation of the spec): require a test plan
			# if there are 0 tests.
			tap_error "Does not appear to be TAP: no TAP version, no test plan, no test lines"
		fi
	fi
}

tap13_parse() {
	tap_error() {
		tap_handle_error "Invalid TAP13: $(printf "$@")"
		tap_flush
	}

	tap_readline
	if [[ "$tap_line" != 'TAP version 13' ]]; then
		tap_error 'First line must be %q' 'TAP version 13'
	fi

	local tap_test_re='^(ok|not ok)'"$re_b"'[[:space:]]*([0-9]+)?([^#]*)(#.*)?'
	#                   1                               2        3      4
	#
	# 1: status (required)
	# 2: number (recommended)
	# 3: description (recommended)
	# 4: comment (when necessary)

	local tap_at_end=false
	local tap_plan=
	local tap_prev_test_number=0 tap_test_count=0 # Not the same, tests can be out-of-order
	local tap_test_status tap_test_number tap_test_description tap_test_comment tap_test_yaml tap_test_directives
	while tap_readline; do
		if [[ "$tap_line" = '#'* ]]; then
			tap_handle_comment "$tap_line"
		elif $tap_at_end; then
			tap_error 'Cannot have more output after trailing test plan'
		elif [[ "$tap_line" = '1..'* ]]; then
			if [[ -n "$tap_plan" ]]; then
				tap_error 'Test plan can only be given once'
				continue
			fi
			tap_plan=${tap_line#1..}
			if ! [[ $tap_plan =~ ^[[:digit:]]+$ ]]; then
				tap_error 'Not an integer number of tests: %s' "$tap_plan"
				continue
			fi
			if (( tap_test_count > 0 )); then
				tap_at_end=true
			fi
		elif [[ "$tap_line" =~ $tap_test_re ]]; then
			tap_test_status=${BASH_REMATCH[1]}
			tap_test_number=${BASH_REMATCH[2]:-$((tap_prev_test_number+1))}
			tap_test_description=$(trim "${BASH_REMATCH[3]:-}")
			tap_test_comment=${BASH_REMATCH[4]:-}

			tap_test_yaml=''
			if tap_peekline && [[ "$tap_next_line" =~ ^[[:space:]]+---$ ]]; then
				while tap_readline; do
					tap_test_yaml="${tap_test_yaml%x}${tap_line}"$'\nx'
					if [[ $tap_line =~ ^[[:space:]]+\.\.\.$ ]]; then
						break
					fi
				done
				tap_test_yaml="${tap_test_yaml%$'\nx'}"
			fi

			# As a hack for Bash <4.4 considering empty arrays to be unset,
			# always include an empty directive.
			tap_test_directives=('')
			if (shopt -s nocasematch; [[ "$tap_test_comment" =~ ^#\ TODO( .*)?$ ]]); then
				tap_test_directives+=('TODO')
			fi
			if (shopt -s nocasematch; [[ "$tap_test_comment" = '# SKIP'* ]]); then
				tap_test_directives+=('SKIP')
			fi

			tap_handle_testline \
				"$tap_test_status" \
				"$tap_test_number" \
				"$tap_test_description" \
				"$tap_test_comment" \
				"$tap_test_yaml" \
				"${tap_test_directives[@]}"

			tap_prev_test_number=$tap_test_number
			tap_test_count=$((tap_test_count+1))
		elif [[ "$tap_line" = 'Bail out!'* ]]; then
			tap_handle_error "$tap_line"
			# Continue parsing, it will result in better error messages/summaries
		else
			tap_error 'Invalid line: %s' "$tap_line"
		fi
	done

	if [[ -n "$tap_plan" ]]; then
		if (( tap_test_count != tap_plan )); then
			tap_error 'Expected %d tests, got %d' "$tap_plan" "$tap_test_count"
		fi
	fi
}

cmd_stream() {
	local flag
	local arg_name=
	while getopts 'n:' flag; do
	      case "$flag" in
		      n) arg_name=$OPTARG;;
		      *) errusage;;
	      esac
	done
	shift $((OPTIND - 1))
	if (( $# > 0 )); then
		errusage 'Extra arguments: %s' "$*"
	fi
	if [[ -z "$arg_name" ]]; then
		errusage 'Must specify a name with -n'
	fi

	tap_handle_comment() {
		local comment=$1
	}
	tap_handle_testline() {
		local test_status=$1 test_number=$2 test_description=$3 test_comment=$4 test_yaml=$5 test_directives=("${@:6}")

		if in_array SKIP "${test_directives[@]}"; then
			output="${BLUE}SKIP${ALL_OFF}"
		elif in_array TODO "${test_directives[@]}"; then
			case "$test_status" in
				'ok') output="${RED}XPASS${ALL_OFF}";;
				'not ok') output="${GREEN}XFAIL${ALL_OFF}";;
				*) panic;;
			esac
		else
			case "$test_status" in
				'ok') output="${GREEN}PASS${ALL_OFF}";;
				'not ok') output="${RED}FAIL${ALL_OFF}";;
				*) panic;;
			esac
		fi

		output+=": $arg_name $test_number"
		if [[ -n "${test_description}${test_comment}" ]]; then
			output+=" - ${test_description}"
		fi
		printf '%s\n' "$output"

	}
	tap_handle_error() {
		local error=$1
		printf '%s\n' "${PURPLE}ERROR${ALL_OFF}: ${arg_name} - ${error}"
	}

	tap_parse
}

cmd_cat() {
	local file n=0

	printf '%s\n' 'TAP version 13'

	tap_handle_comment() {
		local comment=$1
		printf '%s\n' "$comment"
	}
	tap_handle_testline() {
		local test_status=$1 test_number=$2 test_description=$3 test_comment=$4 test_yaml=$5 test_directives=("${@:6}")
		n=$((n+1))
		printf '%s\n' "$test_status $n $file $test_number - $test_description $test_comment"
		if [[ -n "$test_yaml" ]]; then
			printf '%s\n' "$test_yaml"
		fi
	}
	tap_handle_error() {
		local error=$1
		printf 'Bail out! %s\n' "$file - $error"
	}
	for file in "$@"; do
		tap_parse < "$file"
	done
}

cmd_summarize() {
	if (( $# != 1 )); then
		errusage 'Expected 1 argument, got %d' "$#"
	fi
	local arg_file=$1

	# Parse the .tap files
	declare -i cnt_skip=0
	declare -i cnt_pass=0
	declare -i cnt_fail=0
	declare -i cnt_xpass=0
	declare -i cnt_xfail=0
	declare -i cnt_error=0
	tap_handle_comment() {
		local comment=$1
	}
	tap_handle_testline() {
		local test_status=$1 test_number=$2 test_description=$3 test_comment=$4 test_yaml=$5 test_directives=("${@:6}")

		if in_array SKIP "${test_directives[@]}"; then
			cnt_skip+=1
		elif in_array TODO "${test_directives[@]}"; then
			case "$test_status" in
				'ok') cnt_xpass+=1;;
				'not ok') cnt_xfail+=1;;
				*) panic;;
			esac
		else
			case "$test_status" in
				'ok') cnt_pass+=1;;
				'not ok') cnt_fail+=1;;
				*) panic;;
			esac
		fi
	}
	tap_handle_error() {
		local error=$1
		cnt_error+=1
	}
	tap_parse < "$arg_file"

	# Decide whether they passed on the whole
	local pass=true
	if (( (cnt_fail + cnt_xpass + cnt_error) > 0 )); then
		pass=false
	fi

	# And generate nice pretty output
	colored() {
		printf '%s%s%s\n' "$1" "$(printf "${@:2}")" "$ALL_OFF"
	}

	colored_cnt() {
		local color=$1
		local name=$2
		local cnt=$3

		if (( cnt == 0 )); then
			color=''
		fi
		colored "$color" '# %-6s %3d' "$name:" "$cnt"
	}

	local color
	if $pass; then
		color=$GREEN
	else
		color=$RED
	fi

	colored "$color" '============================================================================'
	colored "$color" 'test-suite summary'
	colored "$color" '============================================================================'
	colored_cnt ''        TOTAL $((cnt_skip+cnt_pass+cnt_fail+cnt_xpass+cnt_xfail+cnt_error))
	colored_cnt "$BLUE"   SKIP  $cnt_skip
	colored_cnt "$GREEN"  PASS  $cnt_pass
	colored_cnt "$GREEN"  XFAIL $cnt_xfail
	colored_cnt "$RED"    FAIL  $cnt_fail
	colored_cnt "$RED"    XPASS $cnt_xpass
	colored_cnt "$PURPLE" ERROR $cnt_error
	colored "$color" '============================================================================'
	if ! $pass; then
		if [[ "$arg_file" != /* ]]; then
			arg_file="./${arg_file}"
		fi
		colored "$color" 'See %s' "$arg_file"
		colored "$color" '============================================================================'
	fi
	$pass
}

main "$@"
