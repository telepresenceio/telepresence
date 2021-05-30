#!/usr/bin/env bash

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
    local operator=""
    local msg=""
    if $is_empty; then
        operator="-n"
        msg="Output was supposed to be empty and it was not"
    else
        operator="-z"
        msg="Output wasn't supposed to be empty and it was"
    fi
    if [ $operator "$output" ]; then
        echo "Failed in step: ${STEP}"
        echo "> $msg"
        exit 1
    fi
}

# Run telepresence login command + reports output
login() {
    local output=`$TELEPRESENCE login`
    if [[ $output != *"Login successful"* ]]; then
        echo "Login Failed in step: ${STEP}"
        exit 1
    fi
}

# Verify that the user logged out and was logged in prior.
verify_logout() {
    # We care about the error here, so we redirect stderr to stdout
    local output=`$TELEPRESENCE logout 2>&1`
    if [[ $output != *"not logged in"* ]]; then
        echo "Login Failed in step: ${STEP}"
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
# Not currently used bc curl doesn't work with preview url bc we need
# cookie system A adds (I think)
get_preview_url() {
    local regex="Preview URL : (https://[^ >]+)"
    if [[ $output =~ $regex ]]; then
        previewurl="${BASH_REMATCH[1]}"
    else
        echo "No Preview URL found"
        exit 1
    fi
}

# Puts intercept id in a variable
get_intercept_id() {
    local header=`grep 'x-telepresence-intercept-id' <<<"$output"`
    #local header=`echo $output | grep 'x-telepresence-intercept-id'`
    local regex="regexp\(\"([a-zA-Z0-9-]+:dataprocessingservice)\""
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
    local image=`kubectl get deployment dataprocessingservice -o "jsonpath={.spec.template.spec.containers[?(@.name=='traffic-agent')].image}"`
    if [[ -z $image ]]; then
        echo "There is no traffic-agent sidecar and there should be"
        exit 1
    fi

    if $present; then
        local image_present=`echo $image | grep 'ambassador-telepresence-agent:'`
        if [[ -z $image_present ]]; then
            echo "Proprietary traffic agent image wasn't used and it should be"
            exit 1
        elif [[ ! -z $TELEPRESENCE_AGENT_IMAGE && $image_present != *$TELEPRESENCE_AGENT_IMAGE ]]; then
            echo "Propietary traffic agent was supposed to have been overridden but it wasn't"
            exit 1
        fi
    else
        local image_present=`echo $image | grep 'tel2:'`
        if [[ -z $image_present ]]; then
            echo "Non-proprietary traffic agent image wasn't used and it should be"
            exit 1
        fi
    fi
}

# Clones amb-code-quickstart-app and applies k8s manifests
setup_demo_app() {
    echo "Applying quick start apps to the cluster"
    kubectl apply -f https://raw.githubusercontent.com/datawire/edgey-corp-nodejs/main/k8s-config/edgey-corp-web-app-no-mapping.yaml
    kubectl wait -n default deploy dataprocessingservice --for condition=available --timeout=90s > $output_location
    kubectl wait -n default deploy verylargejavaservice --for condition=available --timeout=90s > $output_location
    kubectl wait -n default deploy verylargedatastore --for condition=available --timeout=90s > $output_location
}

# Deletes amb-code-quickstart-app *only* if it was created by this script
cleanup_demo_app() {
    kubectl delete -f https://raw.githubusercontent.com/datawire/edgey-corp-nodejs/main/k8s-config/edgey-corp-web-app-no-mapping.yaml
}


##########################################################
#### The beginning of the script                      ####
##########################################################
DEBUG=${DEBUG:-0}
start_time=`date -u +%s`
if [ -z $TELEPRESENCE ]; then
    TELEPRESENCE=`which telepresence`
fi
curl_opts=( -s )

# DEBUG 1 gives you output of higher level commands (e.g. telepresence, kubectl)
if [ $DEBUG -ge 1 ]; then
    output_location=( "/dev/stdout" )
else
    output_location=( "/dev/null" )
fi

# DEBUG 2 provides all the same as 1 + curl ouput and prints out commands
# before they are ran
if [ $DEBUG == 2 ]; then
    curl_opts=( )
    set -x
fi

echo "Using telepresence: "
echo $TELEPRESENCE
$TELEPRESENCE version
echo

# If this environment variable is set, we want to run the smoke tests with that
# agent. But this agent isn't used unless we are logged in, so we unset the
# var here, and will re-set it after we log in.
if [ ! -z $TELEPRESENCE_AGENT_IMAGE ]; then
    TELEPRESENCE_AGENT_OVERRIDE=$TELEPRESENCE_AGENT_IMAGE
    echo "Using Agent Image for tests where you are logged in:"
    echo $TELEPRESENCE_AGENT_IMAGE
    echo
    unset TELEPRESENCE_AGENT_IMAGE
fi

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

kubectl get svc -n default verylargejavaservice > $output_location 2>&1
if [[ "$?" == 0 ]]; then
    echo "verylargejavaservice is present, so assuming rest of demo apps are already present"
else
    echo "Will setup demo app"
    INSTALL_DEMO=true
    read -p "Would you like it to be cleaned up if all tests pass? (y/n)?" choice
    case "$choice" in
        y|Y ) CLEANUP_DEMO=true;;
        * ) ;;
    esac
fi


read -p "Is this configuration okay (y/n)?" choice
case "$choice" in
    y|Y ) echo ":)";;
    n|N ) echo "Exiting..."; exit 1;;
    * ) echo "invalid"; exit 1;;
esac

echo "Okay one more thing. Please login to System A in the window that pops up"
$TELEPRESENCE login > $output_location

# For now this is just telepresence, we should probably
# get a new cluster eventually to really start from scratch
$TELEPRESENCE uninstall --everything > $output_location
if [[ -n "$INSTALL_DEMO" ]]; then
    setup_demo_app
fi

VERYLARGEJAVASERVICE=verylargejavaservice.default:8080
$TELEPRESENCE connect > $output_location

# When the sevice is initially deployed, it can take a few seconds (~7)
# before the service is actually running, so we build in a few retries
# instead of jumping straight to verify_output_empty which exits upon
# failure
for i in {1..20}
do
    output=`curl -m 1 "${curl_opts[@]}" $VERYLARGEJAVASERVICE | grep 'green'`
    if [ -n "$output" ]; then
        break
    else
        echo "output from verylargejavaservice not found so sleeping" > $output_location
        sleep 1
    fi
done
output=`curl "${curl_opts[@]}" $VERYLARGEJAVASERVICE | grep 'green'`
verify_output_empty "${output}" false

STEP=1
###########################################################
#### Step 1 - Verify telepresence list works           ####
###########################################################

output=`$TELEPRESENCE list | grep 'ready to intercept'`
verify_output_empty "${output}" false

finish_step

###########################################################
#### Step 2 - Verify that service has been intercepted ####
###########################################################

curl "${curl_opts[@]}" localhost:3000 > $output_location
if [[ "$?" != 0 ]]; then
    echo "Ensure you have a local version of dataprocessingservice running on port 3000"
    exit
fi

# General note about intercepts, I've found sleeping for 1 second gives time for the
# commands to run and things to propagate. Could probably be optimized to add automatic
# retries but was trying to keep it simple
$TELEPRESENCE intercept dataprocessingservice -p 3000 > $output_location
sleep 1

is_prop_traffic_agent false

output=`curl "${curl_opts[@]}" $VERYLARGEJAVASERVICE | grep 'blue'`
verify_output_empty "${output}" false

$TELEPRESENCE leave dataprocessingservice > $output_location
$TELEPRESENCE intercept dataprocessingservice --port 3000 --preview-url=false --mechanism=tcp > $output_location
sleep 1

is_prop_traffic_agent false

output=`curl "${curl_opts[@]}" $VERYLARGEJAVASERVICE | grep 'blue'`
verify_output_empty "${output}" false
verify_logout

finish_step

###############################################
#### Step 3 - Verify intercept can be seen ####
###############################################

output=`$TELEPRESENCE list | grep 'dataprocessingservice: intercepted'`
verify_output_empty "${output}" false

finish_step

###############################################
#### Step 4 - Verify intercept can be left ####
###############################################

$TELEPRESENCE leave dataprocessingservice > $output_location
output=`$TELEPRESENCE list | grep 'dataprocessingservice: intercepted'`
verify_output_empty "${output}" true

finish_step

###############################################
#### Step 5 - Verify can access svc        ####
###############################################

curl "${curl_opts[@]}" dataprocessingservice.default:3000 > $output_location
if [[ "$?" != 0 ]]; then
    echo "Unable to access service after leaving intercept"
    exit
fi
finish_step


###############################################
#### Step 6 - Verify login prompted        ####
###############################################

if [[ ! -z $TELEPRESENCE_AGENT_OVERRIDE ]]; then
    export TELEPRESENCE_AGENT_IMAGE=$TELEPRESENCE_AGENT_OVERRIDE
    echo "Using $TELEPRESENCE_AGENT_IMAGE as the agent for remainder of tests"
    $TELEPRESENCE quit > $output_location
    $TELEPRESENCE connect > $output_location
fi

$TELEPRESENCE intercept dataprocessingservice --port 3000 --preview-url=true --http-match=all <<<$'verylargejavaservice.default\n8080\nN\n' > $output_location
sleep 1
is_prop_traffic_agent true

# Verify intercept works
output=`$TELEPRESENCE list | grep 'dataprocessingservice: intercepted'`
verify_output_empty "${output}" false

$TELEPRESENCE leave dataprocessingservice > $output_location
# Verify user can logout without error
# Find a better way to determine if a user is logged in
output=`$TELEPRESENCE logout`
verify_output_empty "${output}" true

finish_step

###############################################
#### Step 7 - Verify login on own works    ####
###############################################

login
output=`$TELEPRESENCE intercept dataprocessingservice --port 3000 <<<$'verylargejavaservice.default\n8080\nN\n'`
sleep 1
has_preview_url true
is_prop_traffic_agent true

finish_step

#####################################################
#### Step 8 - Verify selective preview url works ####
#####################################################

has_intercept_id true
has_preview_url true
get_intercept_id
output=`curl "${curl_opts[@]}" $VERYLARGEJAVASERVICE | grep 'blue'`
verify_output_empty "${output}" true

# Gotta figure out how to get a cookie for this to work
#output=`curl $previewurl | grep 'blue'`
#verify_output_empty "${output}" false
output=`curl "${curl_opts[@]}" -H "x-telepresence-intercept-id: ${interceptid}" $VERYLARGEJAVASERVICE | grep 'blue'`
verify_output_empty "${output}" false

$TELEPRESENCE leave dataprocessingservice > $output_location
finish_step

###############################################################
#### Step 9 - licensed selective intercept w/o preview url ####
###############################################################

output=`$TELEPRESENCE intercept dataprocessingservice --port 3000 --preview-url=false`
sleep 1
has_intercept_id true
has_preview_url false
output=`curl "${curl_opts[@]}" -H "x-telepresence-intercept-id: ${interceptid}" $VERYLARGEJAVASERVICE | grep 'blue'`
verify_output_empty "${output}" false

$TELEPRESENCE leave dataprocessingservice > $output_location
finish_step

###############################################
#### Step 10 - licensed intercept all      ####
###############################################

output=`$TELEPRESENCE intercept dataprocessingservice --port 3000 --http-match=all <<<$'verylargejavaservice.default\n8080\nN\n'`
sleep 1
has_intercept_id false
has_preview_url true
output=`curl "${curl_opts[@]}" $VERYLARGEJAVASERVICE | grep 'blue'`
verify_output_empty "${output}" false

$TELEPRESENCE leave dataprocessingservice > $output_location
finish_step

##########################################################
#### Step 11 - licensed intercept all w/o preview url ####
##########################################################

output=`$TELEPRESENCE intercept dataprocessingservice --port 3000 --http-match=all --preview-url=false`
sleep 1
has_intercept_id false
has_preview_url false
output=`curl "${curl_opts[@]}" $VERYLARGEJAVASERVICE | grep 'blue'`
verify_output_empty "${output}" false

$TELEPRESENCE leave dataprocessingservice > $output_location
finish_step


##########################################################
#### Step 12 - licensed uninstall everything          ####
##########################################################

$TELEPRESENCE uninstall --everything > $output_location
verify_logout

finish_step

##########################################################
#### Step 13 - Verfiy version prompts new version     ####
##########################################################
os=`uname -s | awk '{print tolower($0)}'`
echo "Installing an old version of telepresence to /tmp/old_telepresence to verify it prompts for update"
sudo curl "${curl_opts[@]}" -fL https://app.getambassador.io/download/tel2/$os/amd64/0.7.10/telepresence -o /tmp/old_telepresence
sudo chmod +x /tmp/old_telepresence
output=`/tmp/old_telepresence version | grep 'An update of telepresence from version'`
verify_output_empty "${output}" false
echo "Removing old version of telepresence: /tmp/old_telepresence"
sudo rm /tmp/old_telepresence

finish_step

##########################################################
#### The end :)                                       ####
##########################################################

if [[ -n "$CLEANUP_DEMO" ]]; then
    cleanup_demo_app
fi
end_time=`date -u +%s`
let elapsed_time="$end_time-$start_time"
echo "$TELEPRESENCE has been (mostly) smoke tested and took $elapsed_time seconds :)"
echo "Please test a preview URL manually to ensure that is working on the system A side"
