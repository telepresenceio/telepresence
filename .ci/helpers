#!/bin/bash

# Choose colors carefully. If they don't work on both a black
# background and a white background, pick other colors (so white,
# yellow, and black are poor choices).
export RED='\033[1;31m'
export GRN='\033[1;32m'
export BLU='\033[1;34m'
export CYN='\033[1;36m'
export END='\033[0m'

require() {
    if [ -z "${!1}" ]; then
        echo "please set the $1 environment variable" 2>&1
        exit 1
    fi
}

wait_for_ip() ( # use a subshell so the set +x is local to the function
    { set +x; } 2>/dev/null # make the set +x be quiet
    local external_ip=""
    while true; do
        external_ip=$(kubectl get svc -n "$1" "$2" --template="{{range .status.loadBalancer.ingress}}{{.ip}}{{end}}")
        if [ -z "$external_ip" ]; then
            echo "Waiting for external IP..." 1>&2
            sleep 10
        else
            break
        fi
    done
    echo "$external_ip"
)

wait_for_url() ( # use a subshell so the set +x is local to the function
    { set +x; } 2>/dev/null # make the set +x be quiet
    local status=""
    while true; do
        status=$(curl -k -sL -w "%{http_code}" -o /dev/null "$@")
        if [ "$status" != "200" ]; then
            echo "Got $status, waiting for 200..." 1>&2
            sleep 10
        else
            break
        fi
    done
)

wait_for_deployment() ( # use a subshell so the set +x is local to the function
    { set +x; } 2>/dev/null # make the set +x be quiet
    # Check deployment rollout status every 10 seconds (max 10 minutes) until complete.
    local attempts=0
    while true; do
        if kubectl rollout status "deployment/${2}" -n "$1" 1>&2; then
            break
        else
            CRASHING="$(crashLoops ambassador)"
            if [ -n "${CRASHING}" ]; then
                echo "${CRASHING}" 1>&2
                return 1
            fi
        fi

        if [ $attempts -eq 60 ]; then
            echo "deploy timed out" 1>&2
            return 1
        fi

        attempts=$((attempts + 1))
        sleep 10
    done
)

crashLoops() ( # use a subshell so the set +x is local to the function
    { set +x; } 2>/dev/null # make the set +x be quiet
    # shellcheck disable=SC2016
    kubectl get pods -n "$1" -o 'go-template={{range $pod := .items}}{{range .status.containerStatuses}}{{if .state.waiting}}{{$pod.metadata.name}} {{.state.waiting.reason}}{{"\n"}}{{end}}{{end}}{{end}}' | grep CrashLoopBackOff
)

start_cluster() {
    local kubeconfig timeout profile
    kubeconfig=${1}
    timeout=${2:-3600}
    profile=${3:-default}
    if [ -e "${kubeconfig}" ]; then
        echo "cannot get cluster, kubeconfig ${kubeconfig} exists" 1>&2
        return 1
    fi
    curl -s -H "Authorization: bearer ${KUBECEPTION_TOKEN}" "https://sw.bakerstreet.io/kubeception/api/klusters/ci-?generate=true&timeoutSecs=${timeout}&profile=${profile}" -X PUT > "${kubeconfig}"
    printf "${BLU}Acquiring cluster:\n==${END}\n" 1>&2
    cat "${kubeconfig}" 1>&2
    printf "${BLU}==${END}\n" 1>&2
}

await_cluster() {
    local kubeconfig name kconfurl
    kubeconfig=${1}
    name="$(head -1 "${kubeconfig}" | cut -c2-)"
    kconfurl="https://sw.bakerstreet.io/kubeception/api/klusters/${name}"
    wait_for_url "$kconfurl" -H "Authorization: bearer ${KUBECEPTION_TOKEN}"
    curl -s -H "Authorization: bearer ${KUBECEPTION_TOKEN}" "$kconfurl" -o "${kubeconfig}"
}

get_cluster() {
    start_cluster "$@"
    await_cluster "$@"
}

del_cluster() {
    local kubeconfig name
    kubeconfig=${1}
    name="$(head -1 "${kubeconfig}" | cut -c2-)"
    curl -s -H "Authorization: bearer ${KUBECEPTION_TOKEN}" "https://sw.bakerstreet.io/kubeception/api/klusters/${name}" -X DELETE
    rm -f "${kubeconfig}"
}
