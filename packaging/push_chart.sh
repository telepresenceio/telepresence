#!/bin/bash

set -e

# This will be running in automation so we don't want the
# interactive pager to pop up
export AWS_PAGER=""
# The circle job ensures helm is installed, but if for some reason
# someone is running this locally, then alert them if helm isn't
# installed.
if ! command -v helm 2> /dev/null; then
    echo "Helm isn't installed. Instructions at https://helm.sh/docs/intro/install/"
    exit 1
fi

# Get the toplevel dir of the repo so we can run this command
# no matter which directory we are in.
TOP_DIR="$( git rev-parse --show-toplevel)"
echo $TOP_DIR

CHART_VERSION=$(grep version: $TOP_DIR/charts/telepresence/Chart.yaml | awk ' { print $2 }')
PACKAGE_FILE=telepresence-$CHART_VERSION.tgz
CHART_ARTIFACT_DIR=$(mktemp -d)
echo "Artifacts will be stored in ${CHART_ARTIFACT_DIR}"
helm package $TOP_DIR/charts/telepresence -d $CHART_ARTIFACT_DIR

CHART_PACKAGE=$CHART_ARTIFACT_DIR/$PACKAGE_FILE
echo "CHART_PACKAGE is here: ${CHART_PACKAGE}"

bucket_dir=
if [[ -n "${BUCKET_DIR}" ]]; then
    bucket_dir="${BUCKET_DIR}"
else
    bucket_dir="ambassador-dev/testcharts"
fi

if [ -z "$AWS_BUCKET"] ; then
    AWS_BUCKET=datawire-static-files
fi

if [[ -z "$AWS_ACCESS_KEY_ID" ]] ; then
    echo "AWS_ACCESS_KEY_ID is not set"
    exit 1
elif [[ -z "$AWS_SECRET_ACCESS_KEY" ]]; then
    echo "AWS_SECRET_ACCESS_KEY is not set"
    exit 1
fi

echo "Ensuring chart hasn't already been pushed"
# We don't need the whole object, we just need the metadata
# to see if it exists or not, so this is better than requesting
# the whole tar file.
aws s3api head-object \
    --bucket $AWS_BUCKET \
    --key "${bucket_dir}/$PACKAGE_FILE" || not_exist=true

if [[ -z $not_exist ]]; then
    echo "Chart ${bucket_dir}/$PACKAGE_FILE has already been pushed."
    exit 1

fi

# We only push the chart to the S3 bucket. There will be another process
# S3 side that will re-generate the helm chart index when new objects are
# added.
echo "Pushing chart to S3 bucket $AWS_BUCKET"
echo "Pushing ${bucket_dir}/$PACKAGE_FILE"
aws s3api put-object \
    --bucket "$AWS_BUCKET" \
    --key "${bucket_dir}/$PACKAGE_FILE" \
    --body "$CHART_PACKAGE" && echo "Successfully pushed ${bucket_dir}/$PACKAGE_FILE"

# Clean up
rm -rf $CHART_ARTIFACT_DIR

exit 0
