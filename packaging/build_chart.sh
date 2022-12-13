#!/bin/bash
set -e

# This will be running in automation so we don't want the
# interactive pager to pop up
export AWS_PAGER=""

if [ -z "$1" ]; then
   echo "Must set the destination folder"
   exit 1
fi

DESTINATION="$1"

# Get the toplevel dir of the repo so we can run this command
# no matter which directory we are in.
TOP_DIR="$( git rev-parse --show-toplevel)"

go run "$TOP_DIR/packaging/gen_chart.go" "${DESTINATION}" "${TELEPRESENCE_VERSION}"

package_files=("$DESTINATION"/telepresence-*.tgz)
package_file=${package_files[0]}
echo "${package_file}"

