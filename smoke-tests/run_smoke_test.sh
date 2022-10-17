#!/usr/bin/env bash
# -*- sh-basic-offset: 4 ; indent-tabs-mode: nil -*-

# This is a script for more-or-less automating our smoke tests
# given the scenarios we have currently.
# This should move to our go tests eventually since there is some overlap
# and it's nice to have tests in one place.

# Note that the given step has passsed and bumps global step counter
finish_step() {
    echo "Step ${STEP} success!"
    echo
    ((STEP+=1))
}

# Verify that the output is empty or not depending on args
verify_output_empty() {
    local output=${1}
    local is_empty=${2}
    if $is_empty; then
        if [ -n "$output" ]; then
            echo "Failed in step: ${STEP}"
            echo '> Output was supposed to be empty and it was not'
            exit 1
        fi
    else
        if [ -z "$output" ]; then
            echo "Failed in step: ${STEP}"
            echo "> Output wasn't supposed to be empty and it was"
            exit 1
        fi
    fi
}

# Run telepresence login command + reports output
login() {
    local output
    output=$($TELEPRESENCE login)
    if [[ $output != *"Login successful"* ]]; then
        echo "Login Failed in step: ${STEP}"
        exit 1
    fi
}

# Verify that the user logged out and was logged in prior.
verify_logout() {
    # We care about the error here, so we redirect stderr to stdout
    local output
    output=$($TELEPRESENCE logout 2>&1)
    if [[ $output != *"not logged in"* ]]; then
        echo "Logout Failed in step: ${STEP}"
        exit 1
    fi
}

# Verifies that a Preview URL is or is not in the output
has_preview_url() {
    local present=${1}
    if $present; then
        if [[ "$output" != *"Preview URL"* ]]; then
            echo "Preview URL wasn't present and it should be. Failed step: ${STEP}"
            exit 1
        fi
    else
        if [[ "$output" == *"Preview URL"* ]]; then
            echo "Preview URL was present and it shouldn't be. Failed step: ${STEP}"
            exit 1
        fi
    fi
}

# Verifies that an Intercept ID is or is not in the output
has_intercept_id() {
    local present=${1}
    if $present; then
        if [[ "$output" != *"x-telepresence-intercept-id"* ]]; then
            echo "Intercept id wasn't present and it should be. Failed step: ${STEP}"
            exit 1
        fi
    else
        if [[ "$output" == *"x-telepresence-intercept-id"* ]]; then
            echo "Intercept id was present and it shouldn't be. Failed step: ${STEP}"
            exit 1
        fi
    fi
}

# Puts preview url in a variable
get_preview_url() {
    preview_url=$(echo "$output" | grep -Eo 'https://[^ >]+.preview.edgestack.me')
    if [[ -z $preview_url ]]; then
        echo "No Preview URL found"
        exit 1
    fi
}

# Puts workstation api key in a variable
get_workstation_apikey() {
    local cache_file
    case $os in
    darwin)
        cache_file="$HOME/Library/Caches/telepresence/apikeys.json"
        ;;
    linux)
        cache_file="${XDG_CACHE_HOME:-$HOME/.cache}/telepresence/apikeys.json"
        ;;
    windows)
        cache_file="$HOME/AppData/Local/telepresence/apikeys.json"
        ;;
    *)
        echo "OS is unknown by smoke-tests. Update get_workstation_apikey to include default config location for your OS"
        exit 1
        ;;
  esac
    endpoint="auth.datawire.io"
    if [[ "$SYSTEMA_ENV" == 'staging' ]]; then
        endpoint="staging-auth.datawire.io"
     fi
    apikey=$(jq -r ".[\"$endpoint\"]|.[\"telepresence:agent-http\"]|strings" "$cache_file")
    if [[ -z $apikey ]]; then
        echo "No apikey found"
        exit 1
    fi
}

# Puts intercept id in a variable
get_intercept_id() {
    local header
    header=$(grep 'x-telepresence-intercept-id' <<<"$output")
    #local header=$(echo $output | grep 'x-telepresence-intercept-id')
    local regex=" ([a-zA-Z0-9-]+:dataprocessingservice)'"
    if [[ $header =~ $regex ]]; then
        interceptid="${BASH_REMATCH[1]}"
    else
        echo "No Intercept ID"
        exit 1
    fi
}

# Checks to see if traffic agent is present + proprietary or dummy based on inputs
is_prop_traffic_agent() {
    local present=${1}
    local image
    while [[ $(kubectl get pod -l run=dataprocessingservice --no-headers | wc -l) -gt 1 ]]; do
        kubectl rollout status -n default deploy dataprocessingservice > "$output_location"
        sleep 10
    done
    image=$(kubectl get pod -l run=dataprocessingservice -o "jsonpath={.items[].spec.containers[?(@.name=='traffic-agent')].image}")
    if [[ -z $image ]]; then
        echo "There is no traffic-agent sidecar and there should be"
        exit 1
    fi

    if $present; then
        local image_present
        image_present=$(echo "$image" | grep 'ambassador-telepresence-agent:')
        if [[ -z $image_present ]]; then
            echo "Proprietary traffic agent image wasn't used and it should be"
            exit 1
        elif [[ -n $smart_agent && $image_present != *$smart_agent ]]; then
            echo "Propietary traffic agent was supposed to have been overridden but it wasn't"
            exit 1
        fi
    else
        local image_present
        image_present=$(echo "$image" | grep 'tel2:')
        if [[ -z $image_present ]]; then
            echo "Non-proprietary traffic agent image wasn't used and it should be"
            exit 1
        fi
    fi
}

get_config() {
    if [ -n "$TELEPRESENCE_AGENT_IMAGE" ]; then
        echo "Use images.agentImage in your config.yml to configure the Smart Agent Image to use"
        exit 1
    fi

    case $os in
    darwin)
        config_file="$HOME/Library/Application Support/telepresence/config.yml"
        ;;
    linux)
        config_file="${XDG_CONFIG_HOME:-$HOME/.config}/telepresence/config.yml"
        ;;
    windows)
        config_file="$HOME/AppData/Roaming/telepresence/config.yml"
        ;;
    *)
        echo "OS is unknown by smoke-tests. Update get_config to include default config location for your OS"
        exit 1
        ;;
    esac
    echo "Using config file: "
    yq e '.' "$config_file"
    echo
}

# Clones amb-code-quickstart-app and applies k8s manifests
setup_demo_app() {
    echo "Applying quick start apps to the cluster"
    kubectl apply -f https://raw.githubusercontent.com/datawire/edgey-corp-nodejs/main/k8s-config/edgey-corp-web-app-no-mapping.yaml
    kubectl wait -n default deploy dataprocessingservice --for condition=available --timeout=90s >"$output_location"
    kubectl wait -n default deploy verylargejavaservice --for condition=available --timeout=90s >"$output_location"
    kubectl wait -n default deploy verylargedatastore --for condition=available --timeout=90s >"$output_location"
}

check_dependencies() {
    echo "Checking dependencies..."
    for name in jq kubectl yq
    do
        [[ $(which "$name" 2>/dev/null) ]] || { echo "\"$name\" needs to be installed";deps_errors=1; }
    done
    if [[ "$deps_errors" == 1 ]]; then
        echo "Install the above and re-run smoke tests"
        exit 1
    fi
    echo "OK"
}

# Create overrides necessary for installing the helm chart
# with the correct registry and version.
prepare_helm_release() {
    echo "Using helm chart for traffic-manager installation"

    # Determine if we need to override the registry
    if [[ -n $TELEPRESENCE_REGISTRY ]]; then
        helm_overrides+=("image.registry=$TELEPRESENCE_REGISTRY")
    fi

    # Install the traffic-manager that matches the version of the cli
    helm_overrides+=("image.tag=$oss_tag")

    echo "Using helm overrides:"
    local IFS=","; echo "${helm_overrides[*]}"
}

# Use helm to install the traffic-manager in the cluster
helm_install() {
    local values_file=${1}

    # Determine if we need to override the registry
    if [[ -n $TELEPRESENCE_REGISTRY ]]; then
        helm_overrides+=("image.registry=$TELEPRESENCE_REGISTRY" "agentInjector.registry=$TELEPRESENCE_REGISTRY")
    fi
    local image_name
    local image_tag
    # Disable the shellcheck warning about sed; it's deliberately used to prevent bash incompatibilities
    # shellcheck disable=SC2001
    image_name=$(echo "$current_image" | sed 's/^\([^:]*\):\([^:]*\)$/\1/g')
    # shellcheck disable=SC2001
    image_tag=$(echo "$current_image" | sed 's/^\([^:]*\):\([^:]*\)$/\2/g')
    if [[ -z "$image_name" ]] || [[ -z "$image_tag" ]]; then
      echo "Malformed image \"$current_image\""
      exit 1
    fi

    helm_overrides+=("agentInjector.agentImage.name=$image_name" "agentInjector.agentImage.tag=$image_tag")

    # Clean up any pre-existing helm installation for the traffic-manager
    local output
    output=$(helm list --namespace ambassador | grep 'traffic-manager')
    if [[ -n "$output" ]]; then
        helm uninstall traffic-manager --namespace ambassador >"$output_location" 2>&1
    fi

    mkdir -p build-output
    rm -f build-output/telepresence-*.tgz
    go run ./packaging/gen_chart.go build-output "${oss_tag}"

    local IFS=","
    if [[ -n $values_file ]]; then
        helm install traffic-manager ./build-output/telepresence-*.tgz --wait --namespace ambassador --set "${helm_overrides[*]}" -f "$values_file"  > "$output_location" 2>&1
    else
        helm install traffic-manager ./build-output/telepresence-*.tgz --wait --namespace ambassador --set "${helm_overrides[*]}" > "$output_location" 2>&1
    fi
}

restore_config () {
    config_bak="$config_file.bak"
    if [ -f "$config_file" ]; then
        echo "restoring $config_file.bak to $config_file"
        cp "$config_bak" "$config_file"
        rm "$config_bak"
    fi
}

# Make edits to the config to test license support used
# when in an air-gapped environment.
prepare_license_config_systema_enabled() {
    config_bak="$config_file.bak"
    echo Backing up "$config_file" to "$config_file".bak
    cp "$config_file" "$config_bak"
    trap restore_config EXIT
    current_image=$smart_agent
    # delete the following:
    yq e 'del(.cloud.systemaHost)' -i "$config_file"
    yq e 'del(.cloud.systemaPort)' -i "$config_file"
    yq e 'del(.cloud.skipLogin)' -i "$config_file"

    # and set the following:
    yq e ".images.agentImage = \"$current_image\"" -i "$config_file"
    echo "Using the following config for license testing:"
    yq e '.' "$config_file"
}

prepare_license_config_systema_disabled() {
    config_bak="$config_file.bak"
    echo Backing up "$config_file" to "$config_file".bak
    cp "$config_file" "$config_bak"
    trap restore_config EXIT
    current_image=$smart_agent
    # we update the yaml file directly
    yq e '.cloud.skipLogin = true' -i "$config_file"
    yq e '.cloud.systemaHost = "127.0.0.1"' -i "$config_file"
    yq e '.cloud.systemaPort = 456' -i "$config_file"
    yq e ".images.agentImage = \"$current_image\"" -i "$config_file"
    echo "Using the following config for license testing:"
    yq e '.' "$config_file"
}

# Make edits to the config to test license support used
# when in an air-gapped environment.
prepare_oss_config() {
    config_bak="$config_file.bak"
    echo Backing up "$config_file" to "$config_file".bak
    cp "$config_file" "$config_bak"
    trap restore_config EXIT
    current_image="tel2:$oss_tag"
    # we update the yaml file directly
    yq e '.cloud.skipLogin = true' -i "$config_file"
    yq e '.cloud.systemaHost = "127.0.0.1"' -i "$config_file"
    yq e '.cloud.systemaPort = 456' -i "$config_file"
    yq e ".images.agentImage = \"$current_image\"" -i "$config_file"
    echo "Using the following config for non-license testing:"
    yq e '.' "$config_file"
}

# Deletes amb-code-quickstart-app *only* if it was created by this script
cleanup_demo_app() {
    kubectl delete -f https://raw.githubusercontent.com/datawire/edgey-corp-nodejs/main/k8s-config/edgey-corp-web-app-no-mapping.yaml
}


##########################################################
#### The beginning of the script                      ####
##########################################################
DEBUG=${DEBUG:-0}
CLOSED_PORT=${CLOSED_PORT:-1234}
start_time=$(date -u +%s)
os=$(go env GOOS)

check_dependencies

if [ -z "$TELEPRESENCE" ]; then
    TELEPRESENCE=$(which telepresence)
fi
curl_opts=( -s )

# DEBUG 1 gives you output of higher level commands (e.g. telepresence, kubectl)
if [ "$DEBUG" -ge 1 ]; then
    output_location="/dev/stdout"
else
    output_location="/dev/null"
fi

# DEBUG 2 provides all the same as 1 + curl ouput and prints out commands
# before they are ran
if [ "$DEBUG" == 2 ]; then
    curl_opts=( )
    set -x
fi

semver_regex='([0-9]*\.[0-9]+\.[0-9]+)(\-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\-[0-9]*)?'
# Install the traffic-manager that matches the version of the cli
tp_version_output="$($TELEPRESENCE version | grep Client)"
if [[ $tp_version_output =~ $semver_regex ]]; then
  oss_tag=${BASH_REMATCH[0]}
else
  echo "Unable to parse $tp_version_output into a semantic version"
  exit 1
fi

echo "Using telepresence: "
echo "  $TELEPRESENCE"
echo "  $oss_tag"
echo
get_config

if [ -f "$config_file" ]; then
    smart_agent=$(sed -n -e 's/^[ ]*agentImage\:[ ]*//p' "$config_file")
    echo "Smart agent: $smart_agent"
    config_bak="$config_file.bak"
    echo
else
    echo "Please set the images.agentImage to the desired smart agent"
    exit 1
fi

prepare_oss_config
trap restore_config EXIT

echo "Using kubectl: "
which kubectl
kubectl version
echo

echo "Using kubeconfig: "
echo "${KUBECONFIG}"
echo

echo "Using context: "
kubectl config current-context
echo

if [[ -z $TELEPRESENCE_LICENSE ]]; then
    echo "To run air-gapped License Tests set TELEPRESENCE_LICENSE"
else
    if kubectl get secrets -n ambassador systema-license >"$output_location" 2>&1; then
        echo "systema-license secret already exists in ambassador namespace. Remove it before proceeding"
        exit 1
    fi
    echo "Using License: "
    echo "${TELEPRESENCE_LICENSE}"
    # In an air-gapped scenario you need to provide an image to use for
    # the agent. So we either use the latest docker tag we find or the
    # person running tests can provide an image.
    if [[ -z $TELEPRESENCE_LICENSE_AGENT_IMAGE ]]; then
        TELEPRESENCE_LICENSE_AGENT_IMAGE=$(curl https://hub.docker.com/v2/repositories/datawire/ambassador-telepresence-agent/tags | jq -r --slurp '.[0].results[0].name')
    fi
    echo "Using License Agent Image for steps 14 and 15: "
    echo "${TELEPRESENCE_LICENSE_AGENT_IMAGE}"
    echo
    USE_CHART="true" # At this point we're guaranteed to use it
fi

echo "Ensuring port $CLOSED_PORT is closed"
output=$(curl localhost:"$CLOSED_PORT" 2>&1 | grep 'Failed to connect to localhost port')
if [ -z "$output" ]; then
    echo "A service is listening on $CLOSED_PORT, kill it or set CLOSED_PORT to something else."
    exit 1
fi
echo

helm_overrides=()
if [[ -n $USE_CHART ]]; then
    prepare_helm_release
fi

if kubectl get svc -n default verylargejavaservice >"$output_location" 2>&1; then
    echo "verylargejavaservice is present, so assuming rest of demo apps are already present"
else
    echo "Will setup demo app"
    INSTALL_DEMO=true
    read -r -p "Would you like it to be cleaned up if all tests pass? (y/n)?" choice
    case "$choice" in
        y|Y ) CLEANUP_DEMO=true;;
        * ) ;;
    esac
fi


read -r -p "Is this configuration okay (y/n)?" choice
case "$choice" in
    y|Y ) echo ":)";;
    n|N ) echo "Exiting..."; exit 1;;
    * ) echo "invalid"; exit 1;;
esac

$TELEPRESENCE quit -ru

# For now this is just telepresence, we should probably
# get a new cluster eventually to really start from scratch
if (helm list -n ambassador | grep traffic-manager); then
    $TELEPRESENCE helm uninstall >"$output_location"
else
    $TELEPRESENCE quit -ru >"$output_location"
fi
if [[ -n "$INSTALL_DEMO" ]]; then
    setup_demo_app
fi

if [[ -n "$USE_CHART" ]]; then
    helm_install
fi

VERYLARGEJAVASERVICE=verylargejavaservice.default:8080
$TELEPRESENCE helm install >"$output_location"
$TELEPRESENCE connect >"$output_location"
$TELEPRESENCE loglevel debug --duration 5m >"$output_location"

# When the sevice is initially deployed, it can take a few seconds (~7)
# before the service is actually running, so we build in a few retries
# instead of jumping straight to verify_output_empty which exits upon
# failure
#
# shellcheck disable=SC2034
for i in {1..20}; do
    output=$(curl -m 1 "${curl_opts[@]}" $VERYLARGEJAVASERVICE | grep 'green')
    if [ -n "$output" ]; then
        break
    else
        echo "output from verylargejavaservice not found so sleeping" >"$output_location"
        sleep 1
    fi
done
output=$(curl "${curl_opts[@]}" $VERYLARGEJAVASERVICE | grep 'green')
verify_output_empty "${output}" false

STEP=1
###########################################################
#### Step 1 - Verify telepresence list works           ####
###########################################################

output=$($TELEPRESENCE list | grep 'ready to intercept')
verify_output_empty "${output}" false

finish_step

###########################################################
#### Step 2 - Verify that service has been intercepted ####
###########################################################

if ! curl "${curl_opts[@]}" localhost:3000 >"$output_location"; then
    echo "Ensure you have a local version of dataprocessingservice running on port 3000"
    exit
fi

# General note about intercepts, I've found sleeping for 1 second gives time for the
# commands to run and things to propagate. Could probably be optimized to add automatic
# retries but was trying to keep it simple
$TELEPRESENCE intercept dataprocessingservice -p 3000 >"$output_location"
sleep 1

is_prop_traffic_agent false

output=$(curl "${curl_opts[@]}" $VERYLARGEJAVASERVICE | grep 'blue')
verify_output_empty "${output}" false

$TELEPRESENCE leave dataprocessingservice >"$output_location"
# Give the mount time to be removed before we create a new intercept
sleep 3
$TELEPRESENCE intercept dataprocessingservice --port 3000 --preview-url=false --mechanism=tcp >"$output_location"
sleep 1

is_prop_traffic_agent false

output=$(curl "${curl_opts[@]}" $VERYLARGEJAVASERVICE | grep 'blue')
verify_output_empty "${output}" false
verify_logout

finish_step

###############################################
#### Step 3 - Verify intercept can be seen ####
###############################################

output=$($TELEPRESENCE list | grep 'dataprocessingservice: intercepted')
verify_output_empty "${output}" false

finish_step

###############################################
#### Step 3b (temp) - Verify mount works   ####
###############################################
# Due to some issues with newer macOS executors (it could be a macos problem)
# macfuse doesn't work in our integration tests, so we ensure that mounts work
# here. The integration tests *do* test mounts on Windows and Linux so this
# testing is really being extra cautious. We can remove this whole step if/when
# the macfuse issue is cleared up in the macos executors.
mount_path=$($TELEPRESENCE list --output json | jq -r '.stdout | .[] | select(.name=="dataprocessingservice") | .intercept_infos[0].client_mount_point')
if [[ -z $mount_path ]]; then
    echo "Mount path was empty and it shouldn't have been"
    exit 1
fi

if ! stat "$mount_path"/var > "$output_location" 2>&1; then
    echo "The mount was unsuccessful"
    exit 1
fi

###############################################
#### Step 4 - Verify intercept can be left ####
###############################################

$TELEPRESENCE leave dataprocessingservice >"$output_location"
output=$($TELEPRESENCE list | grep 'dataprocessingservice: intercepted')
verify_output_empty "${output}" true

finish_step

###############################################
#### Step 5 - Verify can access svc        ####
###############################################

if ! curl "${curl_opts[@]}" dataprocessingservice.default:3000 >"$output_location"; then
    echo "Unable to access service after leaving intercept"
    exit
fi
finish_step

#############################################################################
#### Step 6 - Verify can intercept service without local process running ####
#############################################################################
sleep 1
$TELEPRESENCE intercept dataprocessingservice --port "$CLOSED_PORT" --preview-url=false --mechanism=tcp >"$output_location"
sleep 1

is_prop_traffic_agent false

output=$($TELEPRESENCE list | grep 'dataprocessingservice: intercepted')
verify_output_empty "${output}" false

$TELEPRESENCE leave dataprocessingservice >"$output_location"
output=$($TELEPRESENCE list | grep 'dataprocessingservice: intercepted')
verify_output_empty "${output}" true

finish_step

###############################################
#### Step 7 - Verify login prompted        ####
###############################################
yq e ".licenseKey.value = \"$TELEPRESENCE_LICENSE\"" smoke-tests/license-values-tpl.yaml > smoke-tests/license-values.yaml

# Now we need to update the config for license workflow
if [[ -n "$USE_CHART" ]]; then
    $TELEPRESENCE logout > "$output_location"
    $TELEPRESENCE quit -ru > "$output_location"
    helm uninstall -n ambassador traffic-manager > "$output_location"
else
    $TELEPRESENCE helm uninstall > "$output_location"
fi
verify_logout

restore_config
prepare_license_config_systema_enabled
helm_install "smoke-tests/license-values.yaml"
echo "Using the following config for remainder of tests:"
yq e '.' "$config_file"
$TELEPRESENCE connect > "$output_location"

$TELEPRESENCE intercept dataprocessingservice --port 3000 --preview-url=true --http-header=all --ingress-host verylargejavaservice.default --ingress-port 8080 --ingress-l5 verylargejavaservice.default >"$output_location"
sleep 1
is_prop_traffic_agent true

# Verify intercept works
output=$($TELEPRESENCE list)
verify_output_empty "${output}" false
output=$($TELEPRESENCE list | grep 'dataprocessingservice: intercepted')
verify_output_empty "${output}" false

$TELEPRESENCE leave dataprocessingservice >"$output_location"
# Verify user can logout without error
# Find a better way to determine if a user is logged in
output=$($TELEPRESENCE logout)
verify_output_empty "${output}" true

finish_step

###############################################
#### Step 8 - Verify login on own works    ####
###############################################

login
$TELEPRESENCE connect > "$output_location"
sleep 5 # avoid known agent mechanism-args race
output=$($TELEPRESENCE intercept dataprocessingservice --port 3000 --ingress-host verylargejavaservice.default --ingress-port 8080 --ingress-l5 verylargejavaservice.default)
sleep 1
has_preview_url true
is_prop_traffic_agent true

finish_step

#####################################################
#### Step 9 - Verify selective preview url works ####
#####################################################

has_intercept_id true
has_preview_url true

get_intercept_id
get_preview_url
get_workstation_apikey

output=$(curl "${curl_opts[@]}" $VERYLARGEJAVASERVICE | grep 'blue')
verify_output_empty "${output}" true

# We probably don't need this but we also check using the intercept-id header
output=$(curl "${curl_opts[@]}" -H "x-telepresence-intercept-id: ${interceptid}" $VERYLARGEJAVASERVICE | grep 'blue')
verify_output_empty "${output}" false

# Verify the preview url works
output=$(curl "${curl_opts[@]}" -H "x-ambassador-api-key: $apikey" "$preview_url"  | grep 'blue')
verify_output_empty "${output}" false

$TELEPRESENCE leave dataprocessingservice >"$output_location"
finish_step

###############################################################
#### Step 10 - licensed selective intercept w/o preview url ####
###############################################################

sleep 15 # avoid known agent mechanism-args race
output=$($TELEPRESENCE intercept dataprocessingservice --port 3000 --preview-url=false)
sleep 1
has_intercept_id true
has_preview_url false
output=$(curl "${curl_opts[@]}" -H "x-telepresence-intercept-id: ${interceptid}" $VERYLARGEJAVASERVICE | grep 'blue')
verify_output_empty "${output}" false

$TELEPRESENCE leave dataprocessingservice >"$output_location"
finish_step

###############################################
#### Step 11 - licensed intercept all      ####
###############################################

sleep 5 # avoid known agent mechanism-args race
output=$($TELEPRESENCE intercept dataprocessingservice --port 3000 --http-header=all --ingress-host verylargejavaservice.default --ingress-port 8080 --ingress-l5 verylargejavaservice.default)
sleep 1
get_preview_url
has_intercept_id false
has_preview_url true

# Verify preview url goes to the intercepted service
output=$(curl "${curl_opts[@]}" -H "x-ambassador-api-key: $apikey" "$preview_url"  | grep 'blue')
verify_output_empty "${output}" false

# Verify normal traffic goes to intercepted service
output=$(curl "${curl_opts[@]}" $VERYLARGEJAVASERVICE | grep 'blue')
verify_output_empty "${output}" false

$TELEPRESENCE leave dataprocessingservice >"$output_location"
finish_step

##########################################################
#### Step 12 - licensed intercept all w/o preview url ####
##########################################################

sleep 1
output=$($TELEPRESENCE intercept dataprocessingservice --port 3000 --http-header=all --preview-url=false)
sleep 1
has_intercept_id false
has_preview_url false
output=$(curl "${curl_opts[@]}" $VERYLARGEJAVASERVICE | grep 'blue')
verify_output_empty "${output}" false

$TELEPRESENCE leave dataprocessingservice >"$output_location"
finish_step

#############################################################################
#### Step 13 - Verify can intercept service without local process running ####
#############################################################################
sleep 1
$TELEPRESENCE intercept dataprocessingservice --port "$CLOSED_PORT" --preview-url=false >"$output_location"
sleep 1

is_prop_traffic_agent true

output=$($TELEPRESENCE list | grep 'dataprocessingservice: intercepted')
verify_output_empty "${output}" false

$TELEPRESENCE leave dataprocessingservice >"$output_location"
output=$($TELEPRESENCE list | grep 'dataprocessingservice: intercepted')
verify_output_empty "${output}" true

finish_step


##########################################################
#### Step 14 - licensed uninstall everything          ####
##########################################################

# We should be able to uninstall via this command even if the helm chart was used,
# and this should log the user out.
$TELEPRESENCE helm uninstall > "$output_location"
verify_logout

finish_step

##########################################################
#### Step 15 - Verify version prompts new version     ####
##########################################################

# We skip this on windows because that entails downloading a zip and installing it.
if [[ "$os" == "linux" || $os == "darwin" ]]; then
    echo "Installing an old version of telepresence to /tmp/old_telepresence to verify it prompts for update"
    sudo curl "${curl_opts[@]}" -fL "https://app.getambassador.io/download/tel2/$os/amd64/0.7.10/telepresence" -o /tmp/old_telepresence
    sudo chmod +x /tmp/old_telepresence
    output=$(/tmp/old_telepresence version | grep 'An update of telepresence from version')
    verify_output_empty "${output}" false
    echo "Removing old version of telepresence: /tmp/old_telepresence"
    sudo rm /tmp/old_telepresence
fi
finish_step

if [[ -n $TELEPRESENCE_LICENSE ]]; then
    ##########################################################
    #### Step 16 - Verify License Workflow (helm)         ####
    ##########################################################
    # Now we need to update the config for license workflow

    # Need to uninstall in case the traffic agent webhook image has changed
    $TELEPRESENCE helm uninstall -e > "$output_location"
    restore_config
    prepare_license_config_systema_disabled
    helm_install "smoke-tests/license-values.yaml"

    # Ensure we can intercept a persona intercept and that it works with the license
    output=$($TELEPRESENCE intercept dataprocessingservice --port 3000 --preview-url=false --http-header=auto)
    sleep 15
    # Ensure we aren't logged in since we are testing air-gapped license support
    verify_logout
    has_intercept_id true
    has_preview_url false
    get_intercept_id

    # Ensure when using the interceptid in the header, we get the intercepted service
    output=$(curl "${curl_opts[@]}" -H "x-telepresence-intercept-id: ${interceptid}" $VERYLARGEJAVASERVICE | grep 'blue')
    verify_output_empty "${output}" false

    # Ensure all other requests are going to the service in the cluster
    output=$(curl "${curl_opts[@]}" $VERYLARGEJAVASERVICE | grep 'green')
    verify_output_empty "${output}" false

    $TELEPRESENCE leave dataprocessingservice >"$output_location"
    finish_step

    ##########################################################
    #### Step 17 - Verify Invalid License Behavior (helm) ####
    ##########################################################
    $TELEPRESENCE quit -ru >"$output_location"
    helm uninstall traffic-manager --namespace ambassador > "$output_location" 2>&1

    expired_license="eyJhbGciOiJSUzI1NiJ9.eyJhY2NvdW50SWQiOiJjOWQxYmMwMi1iOWYyLTQ3NW\
    EtODM3OS1iZTk1YTcxOWIyNGIiLCJhdWQiOlsiNTgyNjU2ZmYtMDU0Yy00NzRkLTg0MWYtYTk0YzYyOD\
    JmOWU3Il0sImRlc2NyaXB0aW9uIjoiZG9ubnl5dW5nLTEiLCJleHAiOjE2MzUwMjE4MzAsImlhdCI6MT\
    YxOTQ2OTgzMCwiaXNzIjoiYW1iYXNzYWRvci1jbG91ZCIsImxpbWl0cyI6e30sInN1YiI6IjNiMzFkMT\
    hmLWU2N2ItNGVhNS1hYzc0LTcwYmUzNjMwZTZlZiJ9.tohxXi-lgU3e7gd2IEdg1UiP-HkstvBCJARaa\
    jvWqykG2mdeZbquNwsl_IgKT_RYrQLmcMUZ4k5KEU1eqNhupFzqqI3CvNa_dlRIsUFvyJ2BEUxORS_1p\
    YnNHa1psEe7iuyOK97NgVVRtgJ-6iwf9Phf-YB9WNfePaw7ipdPh6BS2H_ZXk3h7C4fZP_qz8nzr2EBy\
    GQejpEQLxEzY63qbZmXj5ZD0frec-Jkl344gxx2ZwYnmH3UhZcPVzEHpezULspr_YTRHa93vDP6NJ-Cv\
    lfhTgZif_1yjMh5KrvsLgPusNvDT5BUchmqhUow_8ev7n1SMme4zRKG3mTfbwsjuw"

    yq e ".licenseKey.value = \"${expired_license}\"" smoke-tests/license-values-tpl.yaml > smoke-tests/license-values.yaml

    # Now we need to update the config for license workflow
    helm_install "smoke-tests/license-values.yaml"
    $TELEPRESENCE connect > "$output_location"

    # Ensure we can intercept a persona intercept and that it works with the license
    output=$($TELEPRESENCE intercept dataprocessingservice --port 3000 --preview-url=false --http-header=auto 2>&1)
    sleep 1

    # Ensure we aren't logged in since we are testing air-gapped license support
    verify_logout
    error_present=$(echo "$output" | grep 'intercept was made from an unauthenticated client')
    if [[ -z $error_present ]]; then
        echo "Intercept should have errored since the license is invalid"
        exit 1
    fi
    finish_step

    ##########################################################
    ###########  Step 18 - Verify Cloud Intercept  ###########
    ##########################################################
    echo "Step ${STEP}: log into https://getambassador.io/cloud and start an intercept."
    read -r -p "Success(y/n)? " yn
    case $yn in
        [Yy]* )	finish_step;; 
        [Nn]* ) echo "Should be able to initiate intercept from the cloud"; exit 1;;
        * ) echo "invalid input"; exit 1;;
    esac


    $TELEPRESENCE helm uninstall >"$output_location"
    finish_step
    restore_config
    trap - EXIT

fi
##########################################################
#### The end :)                                       ####
##########################################################

if [[ -n "$CLEANUP_DEMO" ]]; then
    cleanup_demo_app
fi
end_time=$(date -u +%s)
elapsed_time=$((end_time - start_time))
echo "$TELEPRESENCE has been smoke tested and took $elapsed_time seconds :)"
