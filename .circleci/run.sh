#!/bin/bash

if [ -z "${CIRCLE_TOKEN}" ]; then
    echo "For this script to work you need to create a circle-ci personal access token and put it in the CIRCLE_TOKEN environment variable."
    exit 1
fi

SOURCE="${BASH_SOURCE[0]}"
while [ -h "$SOURCE" ]; do # resolve $SOURCE until the file is no longer a symlink
  DIR="$( cd -P "$( dirname "$SOURCE" )" && pwd )"
  SOURCE="$(readlink "$SOURCE")"
  [[ $SOURCE != /* ]] && SOURCE="$DIR/$SOURCE" # if $SOURCE was a relative symlink, we need to resolve it relative to the path where the symlink file was located
done
DIR="$( cd -P "$( dirname "$SOURCE" )" && pwd )"

curl --user ${CIRCLE_TOKEN}: \
    --request POST \
    --form revision=HEAD \
    --form config=@${DIR}/config.yml \
    --form notify=false \
        https://circleci.com/api/v1.1/project/github/datawire/teleproxy/tree/master
