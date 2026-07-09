#!/usr/bin/env bash
#
# check-integration-retry.sh: drives up to three cycles of
# `make check-integration`, scoping each retry to the previous cycle's
# failures. Invoked by the `check-integration-ci` make target from the
# repository root; never calls `go test` itself.
#
# Usage:
#   check-integration-retry.sh <test-report-bin>
#   check-integration-retry.sh --scope-only <test-report-bin> <failures-file>
#
# Contract:
#   - Attempt 1 honors any TEST_SUITE/TEST_NAME already in the environment.
#     Attempts 2 and 3 are scoped from the previous attempt's failures file,
#     or run fully unscoped when that file is missing, incomplete, or the
#     failure set carries no suite/method information.
#   - Attempt N writes two files to the workspace root: the rendered test
#     log (check-integration-attempt-N.log) and the JSON failures document
#     (check-integration-attempt-N-failures.log). Both use a .log suffix so
#     the upload-logs action collects them on every OS.
#   - Exits 0 as soon as an attempt reports no failures (make exits 0).
#     Exits 1 after a third failing attempt.
#   - Always prints a summary (stdout, and GITHUB_STEP_SUMMARY when set)
#     before exiting, listing each attempt's failures and, when jq is
#     available, classifying tests that failed earlier but not in the last
#     executed attempt as FLAKY and tests still failing in it as PERSISTENT.

set -u -o pipefail

MAX_ATTEMPTS=3
ROOT=$(pwd)

SCRATCH=$(mktemp -d)
trap 'rm -rf "$SCRATCH"' EXIT

# compute_scope runs `test-report -scope` on a failures file and sets
# CUR_SUITE / CUR_NAME to the parsed TEST_SUITE / TEST_NAME values. Both are
# left empty (unscoped) when the file is missing/incomplete, -scope errors,
# or the failure set is unscopeable.
compute_scope() {
  failures_file=$1
  CUR_SUITE=
  CUR_NAME=
  scope_out=
  if scope_out=$("$TEST_REPORT_BIN" -scope "$failures_file" 2>/dev/null); then
    while IFS='=' read -r key value; do
      case "$key" in
      TEST_SUITE) CUR_SUITE=$value ;;
      TEST_NAME) CUR_NAME=$value ;;
      esac
    done <<<"$scope_out"
  fi
}

# failures_set prints "suite<TAB>method" lines, sorted and de-duplicated, for
# a failures file. Only annotated entries participate: the suite and method
# carry no per-run namespace segment, so unlike the raw test paths they are
# comparable across attempts. Prints nothing when jq is unavailable or the
# file is missing/unreadable.
failures_set() {
  f=$1
  command -v jq >/dev/null 2>&1 || return 0
  [ -f "$f" ] || return 0
  jq -r '.failures[] | select(.suite != null) | "\(.suite)\t\(.method // "")"' "$f" 2>/dev/null | sort -u
}

# print_summary renders the per-attempt failure lists and, when jq is
# available, the flaky/persistent classification, to stdout and (when set)
# GITHUB_STEP_SUMMARY.
print_summary() {
  last=$1
  out="$SCRATCH/summary.md"
  {
    echo "## check-integration-ci summary"
    echo
    for ((i = 1; i <= last; i++)); do
      f="$ROOT/check-integration-attempt-$i-failures.log"
      echo "### Attempt $i"
      if [ -f "$f" ] && command -v jq >/dev/null 2>&1; then
        count=$(jq '.failures | length' "$f" 2>/dev/null)
        if [ "$count" = "0" ]; then
          echo "- no failures"
        elif [ -n "$count" ]; then
          # Annotated entries are the actionable failures; the rest are
          # prefix-rollups of these, shown only when nothing was annotated
          # (a harness-level failure).
          annotated=$(jq -r '.failures[] | select(.suite != null) | "- `\(.suite)\(if .method then "." + .method else " (suite setup)" end)`"' "$f")
          if [ -n "$annotated" ]; then
            echo "$annotated"
          else
            jq -r '.failures[] | "- `\(.test)` (`\(.package)`)"' "$f"
          fi
        else
          echo "- failures file unreadable"
        fi
      elif [ -f "$f" ]; then
        echo '```json'
        cat "$f"
        echo '```'
      else
        echo "- no failures file (infrastructure failure; next attempt ran unscoped)"
      fi
      echo
    done

    if command -v jq >/dev/null 2>&1; then
      last_set="$SCRATCH/last.set"
      failures_set "$ROOT/check-integration-attempt-$last-failures.log" >"$last_set"

      flaky="$SCRATCH/flaky.set"
      : >"$flaky"
      for ((j = 1; j < last; j++)); do
        earlier_set="$SCRATCH/attempt-$j.set"
        failures_set "$ROOT/check-integration-attempt-$j-failures.log" >"$earlier_set"
        comm -23 "$earlier_set" "$last_set" >>"$flaky"
      done
      sort -u -o "$flaky" "$flaky"

      echo "### Flaky (failed in an earlier attempt, passed by attempt $last)"
      if [ -s "$flaky" ]; then
        while IFS=$'\t' read -r suite method; do
          echo "- \`$suite${method:+.$method}\`"
        done <"$flaky"
      else
        echo "- none"
      fi
      echo

      echo "### Persistent (failing in attempt $last)"
      if [ -s "$last_set" ]; then
        while IFS=$'\t' read -r suite method; do
          echo "- \`$suite${method:+.$method}\`"
        done <"$last_set"
      else
        echo "- none"
      fi
    else
      echo "_jq not found; flaky/persistent classification skipped._"
    fi
  } >"$out"

  cat "$out"
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    cat "$out" >>"$GITHUB_STEP_SUMMARY"
  fi
}

if [ "${1:-}" = "--scope-only" ]; then
  TEST_REPORT_BIN=${2:?usage: check-integration-retry.sh --scope-only <test-report-bin> <failures-file>}
  compute_scope "${3:?usage: check-integration-retry.sh --scope-only <test-report-bin> <failures-file>}"
  if [ -n "$CUR_SUITE" ]; then
    echo "TEST_SUITE=$CUR_SUITE"
    [ -n "$CUR_NAME" ] && echo "TEST_NAME=$CUR_NAME"
  else
    echo "UNSCOPED"
  fi
  exit 0
fi

TEST_REPORT_BIN=${1:?usage: check-integration-retry.sh <test-report-bin>}

CUR_SUITE=${TEST_SUITE:-}
CUR_NAME=${TEST_NAME:-}

last_attempt=0
status=1
for ((attempt = 1; attempt <= MAX_ATTEMPTS; attempt++)); do
  last_attempt=$attempt
  log_file="$ROOT/check-integration-attempt-$attempt.log"
  failures_file="$ROOT/check-integration-attempt-$attempt-failures.log"

  make_args=(check-integration "TEST_FAILFAST=false" "TEST_FAILURES_OUT=$failures_file" "TEST_LOG_OUTPUT=$log_file")
  [ -n "$CUR_SUITE" ] && make_args+=("TEST_SUITE=$CUR_SUITE")
  [ -n "$CUR_NAME" ] && make_args+=("TEST_NAME=$CUR_NAME")

  echo "==> check-integration-ci: attempt $attempt/$MAX_ATTEMPTS (TEST_SUITE=${CUR_SUITE:-<all>} TEST_NAME=${CUR_NAME:-<all>})"
  make "${make_args[@]}"
  status=$?

  if [ "$status" -eq 0 ]; then
    break
  fi
  if [ "$attempt" -eq "$MAX_ATTEMPTS" ]; then
    break
  fi
  compute_scope "$failures_file"
done

print_summary "$last_attempt"
exit "$status"
