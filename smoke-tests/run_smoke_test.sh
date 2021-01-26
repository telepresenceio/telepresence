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
    local regex="regexp\(\"([a-zA-z0-9-]+:dataprocessingnodeservice)\""
    if [[ $header =~ $regex ]]; then
        interceptid="${BASH_REMATCH[1]}"
    else
        echo "No Intercept ID"
        exit 1
    fi
}

# Checks to see if traffic manager is present + proprietary or dummy based on inputs 
is_prop_traffic_manager() {
    local present=${1}
    local image=`kubectl get deployment dataprocessingnodeservice -o "jsonpath={.spec.template.spec.containers[?(@.name=='traffic-agent')].image}"`
    if [[ -z $image ]]; then
        echo "There is no traffic-manager sidecar and there should be"
        exit 1
    fi 

    if $present; then
        local image_present=`echo $image | grep 'ambassador-telepresence-agent:'`
        if [[ -z $image_present ]]; then 
            echo "Proprietary traffic manager image wasn't used and it should be"
            exit 1
        fi
    else
        local image_present=`echo $image | grep 'tel2:'`
        if [[ -z $image_present ]]; then
            echo "Non-proprietary traffic manager image wasn't used and it should be"
            exit 1
        fi
    fi
}

# Waits for ambassador to be up + serving traffic to our dataprocessingnodeservice app
wait_for_ambassador() {
    echo 'Get external ip for ambassador'
    for run in {1..30}; do
        AMBASSADOR_SERVICE_IP=`kubectl get service -n ambassador ambassador -o jsonpath='{.status.loadBalancer.ingress[0].ip}'`
        local output=`curl "${curl_opts[@]}" --connect-timeout 5 $AMBASSADOR_SERVICE_IP | grep 'green'`
        if [[ -n $output ]]; then
            return 0
        fi
        sleep 10
    done
}

# Clones amb-code-quickstart-app and applies k8s manifests
setup_demo_app() {
    echo "Cloning amb-code-quickstart-app to /tmp/amb-code-quickstart-app" 
    git clone git@github.com:datawire/amb-code-quickstart-app.git /tmp/amb-code-quickstart-app > $output_location 2>&1
    kubectl apply -f /tmp/amb-code-quickstart-app/k8s-config/1-aes-crds.yml > $output_location
    kubectl wait --for condition=established --timeout=90s crd -lproduct=aes > $output_location
    kubectl apply -f /tmp/amb-code-quickstart-app/k8s-config/2-aes.yml > $output_location 
    kubectl wait -n ambassador deploy -lproduct=aes --for condition=available --timeout=90s > $output_location
    kubectl apply -f /tmp/amb-code-quickstart-app/k8s-config/edgy-corp-web-app.yaml > $output_location
    kubectl wait -n default deploy dataprocessingnodeservice --for condition=available --timeout=90s > $output_location
}

# Deletes amb-code-quickstart-app *only* if it was created by this script 
cleanup_demo_app() {
    kubectl delete -f /tmp/amb-code-quickstart-app/k8s-config/edgy-corp-web-app.yaml > $output_location
    kubectl delete -f /tmp/amb-code-quickstart-app/k8s-config/2-aes.yml > $output_location 
    kubectl delete -f /tmp/amb-code-quickstart-app/k8s-config/1-aes-crds.yml > $output_location
    rm -rf /tmp/amb-code-quickstart-app
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

kubectl get svc -n ambassador ambassador > $output_location 2>&1
if [[ "$?" == 0 ]]; then 
    echo "Ambassador is installed, so assuming demo apps are already present"
else
    echo "Will setup Ambassador + demo app"
    INSTALL_DEMO=True
    read -p "Would you like it to be cleaned up if all tests pass? (y/n)?" choice
    case "$choice" in 
        y|Y ) CLEANUP_DEMO=True;;
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

wait_for_ambassador
echo $AMBASSADOR_SERVICE_IP
# Verify that service is running in the cluster
output=`curl "${curl_opts[@]}" $AMBASSADOR_SERVICE_IP | grep 'green'`
verify_output_empty "${output}" False

STEP=1
###########################################################
#### Step 1 - Verify telepresence list works           ####
###########################################################

output=`$TELEPRESENCE list | grep 'ready to intercept'`
verify_output_empty "${output}" False

finish_step

###########################################################
#### Step 2 - Verify that service has been intercepted ####
###########################################################

curl "${curl_opts[@]}" localhost:3000 > $output_location
if [[ "$?" != 0 ]]; then 
    echo "Ensure you have a local version of dataprocessingnodeservice running on port 3000"
    exit
fi

# General note about intercepts, I've found sleeping for 1 second gives time for the
# commands to run and things to propagate. Could probably be optimized to add automatic
# retries but was trying to keep it simple
$TELEPRESENCE intercept dataprocessingnodeservice -p 3000 > $output_location
sleep 1

is_prop_traffic_manager False

output=`curl "${curl_opts[@]}" $AMBASSADOR_SERVICE_IP | grep 'blue'`
verify_output_empty "${output}" False

$TELEPRESENCE leave dataprocessingnodeservice > $output_location
$TELEPRESENCE intercept dataprocessingnodeservice --port 3000 --preview-url=false --match=all > $output_location
sleep 1

is_prop_traffic_manager False

output=`curl "${curl_opts[@]}" $AMBASSADOR_SERVICE_IP | grep 'blue'`
verify_output_empty "${output}" False
verify_logout

finish_step

###############################################
#### Step 3 - Verify intercept can be seen ####
###############################################

output=`$TELEPRESENCE list | grep 'dataprocessingnodeservice: intercepted'`
verify_output_empty "${output}" False

finish_step

###############################################
#### Step 4 - Verify intercept can be left ####
###############################################

$TELEPRESENCE leave dataprocessingnodeservice > $output_location
output=`$TELEPRESENCE list | grep 'dataprocessingnodeservice: intercepted'`
verify_output_empty "${output}" True

finish_step

###############################################
#### Step 5 - Verify login prompted        ####
###############################################


# TODO: this is a HACK so we have the login, but don't actually try to make an intercept
# For some reason, if we aren't logged in and pipe in these inputs, the intercept doesn't have a 
# token despite logging in. I think it's likely bc of how I'm piping input into these commands 
# since there isn't a way via the cli: https://github.com/datawire/telepresence2/issues/118
$TELEPRESENCE intercept notrealservice --port 3000 --preview-url=true --match=all <<<$'ambassador.ambassador\n80\nN\n' > $output_location 2>&1
sleep 3
$TELEPRESENCE intercept dataprocessingnodeservice --port 3000 --preview-url=true --match=all <<<$'ambassador.ambassador\n80\nN\n' > $output_location
sleep 1
is_prop_traffic_manager False

# Verify intercept works
output=`$TELEPRESENCE list | grep 'dataprocessingnodeservice: intercepted'`
verify_output_empty "${output}" False

$TELEPRESENCE leave dataprocessingnodeservice > $output_location
# Verify user can logout without error
# Find a better way to determine if a user is logged in
output=`$TELEPRESENCE logout`
verify_output_empty "${output}" True

finish_step

###############################################
#### Step 6 - Verify login on own works    ####
###############################################

login
output=`$TELEPRESENCE intercept dataprocessingnodeservice --port 3000 <<<$'ambassador.ambassador\n80\nN\n'`
sleep 1
has_preview_url True
is_prop_traffic_manager True

finish_step

#####################################################
#### Step 7 - Verify selective preview url works ####
#####################################################

has_intercept_id True
has_preview_url True
get_intercept_id
output=`curl "${curl_opts[@]}" $AMBASSADOR_SERVICE_IP | grep 'blue'`
verify_output_empty "${output}" True

# Gotta figure out how to get a cookie for this to work
#output=`curl $previewurl | grep 'blue'`
#verify_output_empty "${output}" False
output=`curl "${curl_opts[@]}" -H "x-telepresence-intercept-id: ${interceptid}" $AMBASSADOR_SERVICE_IP | grep 'blue'`
verify_output_empty "${output}" False

$TELEPRESENCE leave dataprocessingnodeservice > $output_location
finish_step

###############################################################
#### Step 8 - licensed selective intercept w/o preview url ####
###############################################################

output=`$TELEPRESENCE intercept dataprocessingnodeservice --port 3000 --preview-url=false`
sleep 1
has_intercept_id True
has_preview_url False
output=`curl "${curl_opts[@]}" -H "x-telepresence-intercept-id: ${interceptid}" $AMBASSADOR_SERVICE_IP | grep 'blue'`
verify_output_empty "${output}" False

$TELEPRESENCE leave dataprocessingnodeservice > $output_location
finish_step

###############################################
#### Step 9 - licensed intercept all       ####
###############################################

output=`$TELEPRESENCE intercept dataprocessingnodeservice --port 3000 --match=all <<<$'ambassador.ambassador\n80\nN\n'`
sleep 1
has_intercept_id False
has_preview_url True
output=`curl "${curl_opts[@]}" $AMBASSADOR_SERVICE_IP | grep 'blue'`
verify_output_empty "${output}" False

$TELEPRESENCE leave dataprocessingnodeservice > $output_location
finish_step

##########################################################
#### Step 10 - licensed intercept all w/o preview url ####
##########################################################

output=`$TELEPRESENCE intercept dataprocessingnodeservice --port 3000 --match=all --preview-url=false`
sleep 1
has_intercept_id False
has_preview_url False
output=`curl "${curl_opts[@]}" $AMBASSADOR_SERVICE_IP | grep 'blue'`
verify_output_empty "${output}" False

$TELEPRESENCE leave dataprocessingnodeservice > $output_location
finish_step


##########################################################
#### Step 11 - licensed uninstall everything          ####
##########################################################

$TELEPRESENCE uninstall --everything > $output_location
verify_logout

finish_step

##########################################################
#### Step 12 - Verfiy version prompts new version     ####
##########################################################
os=`uname -s | awk '{print tolower($0)}'`
echo "Installing an old version of telepresence to /tmp/old_telepresence to verify it prompts for update"
sudo curl "${curl_opts[@]}" -fL https://app.getambassador.io/download/tel2/$os/amd64/0.7.10/telepresence -o /tmp/old_telepresence
sudo chmod +x /tmp/old_telepresence
output=`/tmp/old_telepresence version | grep 'An update of telepresence from version'`
verify_output_empty "${output}" False
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
