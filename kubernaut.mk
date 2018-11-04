## Quickstart (and pretty much everything else you need to know)
#
#  1. Put this file in your source tree and include it from your
#     Makefile, e.g.:
#
#     ...
#     include kubernaut.mk
#     ...
#
#  2. Run `make foo.knaut` to (re)acquire a cluster.
#
#  3. Use `kubectl -kubeconfig foo.knaut ...` to use a cluster.
#
#  4. Run `make foo.knaut.clean` to release the cluster.
#
#  5. If you care, the claim name is in foo.knaut.claim. This will use
#     a UUID if the CI environment variable is set.
#
#  6. Incorporate <blah>.knaut[.clean] targets into your Makefile as
#     needed
#
#     tests: test-cluster.knaut
#             KUBECONFIG=test-cluster.knaut py.test ...
#
#     clean: test-cluster.knaut.clean
#
#  7. Use the kubernaut.clobber target to delete the kubernaut binary
#     itself:
#
#     clobber: kubernaut.clobber
#

.PRECIOUS: %.knaut.claim
.SECONDARY: %.knaut.claim

%.knaut.claim :
	@if [ -z $${CI+x} ]; then \
		echo $(@:%.knaut.claim=%)-$${USER} > $@; \
	else \
		echo $(@:%.knaut.claim=%)-$${USER}-$(shell uuidgen) > $@; \
	fi

KUBERNAUT=gubernaut
KUBERNAUT_CLAIM_FILE=$(@:%.knaut=%.knaut.claim)
KUBERNAUT_CLAIM_NAME=$(shell cat $(KUBERNAUT_CLAIM_FILE))

%.knaut : %.knaut.claim $(KUBERNAUT)
	$(KUBERNAUT) -release $(KUBERNAUT_CLAIM_NAME)
	$(KUBERNAUT) -claim $(KUBERNAUT_CLAIM_NAME) -output $@

.PHONY: %.knaut.clean

%.knaut.clean : $(KUBERNAUT)
	if [ -e $(@:%.clean=%.claim) ]; then $(KUBERNAUT) -release $$(cat $(@:%.clean=%.claim)); fi
	rm -f $(@:%.knaut.clean=%.knaut)
	rm -f $(@:%.clean=%.claim)

.PHONY: kubernaut.clobber

kubernaut.clobber:
	rm -f gubernaut gubernaut.go

gubernaut.go:
	echo "$${GUBERNAUT}" > gubernaut.go

gubernaut: gubernaut.go
	go build gubernaut.go

export GUBERNAUT
define GUBERNAUT
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
)

func claims(method, name, body string) Response {
	req, err := http.NewRequest(method, "https://next.kubernaut.io/claims" + name, strings.NewReader(body))
	if err != nil { log.Fatal(err) }
	req.Header.Add("Authorization", "Bearer " + *token)
	req.Header.Add("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil { log.Fatal(err) }
	if resp.StatusCode / 100 != 2 {
		log.Fatal(resp)
	}
	respbody, err := ioutil.ReadAll(resp.Body)
	if err != nil { log.Fatal(err) }
	var response Response
	if len(respbody) > 0 {
		err = json.Unmarshal(respbody, &response)
		if err != nil { log.Fatal(err) }
		response.Raw = string(respbody)
	}

	return response
}

type Response struct {
	Claim struct {
		Name string
		Kubeconfig string
	}
	Claims []struct {
		Name string
		ClusterId string
	}
	Raw string
}

var token = flag.String("token", os.Getenv("KUBERNAUT_TOKEN"), "kubernaut API token")
var claim = flag.String("claim", "", "claim name")
var output = flag.String("output", "", "path to write kubeconfig file")
var release = flag.String("release", "", "claim name")
var list = flag.Bool("list", false, "list claims")

func main() {
	flag.Parse()

	if *token == "" {
		log.Fatal("a valid token is required, please put one in the KUBERNAUT_TOKEN env variable")
	}

	if *claim != "" {
		if *output == "" {
			log.Fatal("please specify an output path for your kubeconfig")
		}

		response := claims("POST", "", fmt.Sprintf(`
{
    "name": "%s",
    "group": "main"
}
`, *claim))

		err := ioutil.WriteFile(*output, []byte(response.Claim.Kubeconfig), 0644)
		if err != nil { log.Fatal(err) }
	}

	if *release != "" {
		claims("DELETE", "/" + *release,"")
	}

	if *list {
		response := claims("GET", "", "")
		for _, claim := range response.Claims {
			if claim.ClusterId != "" {
				fmt.Printf("%s: %s\n", claim.Name, claim.ClusterId)
			} else {
				fmt.Printf("%s\n", claim.Name)
			}
		}
	}
}
endef
