package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/datawire/ambassador/pkg/k8s"
	"github.com/datawire/ambassador/pkg/supervisor"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func aesInstallCmd() *cobra.Command {
	res := &cobra.Command{
		Use:   "install",
		Short: "Install the Ambassador Edge Stack in your cluster",
		Args:  cobra.ExactArgs(0),
		RunE:  aesInstall,
	}
	_ = res.Flags().StringP(
		"context", "c", "",
		"The Kubernetes context to use. Defaults to the current kubectl context.",
	)
	_ = res.Flags().BoolP(
		"verbose", "v", false,
		"Show all output. Defaults to sending most output to the logfile.",
	)
	return res
}

func getEmailAddress(defaultEmail string, log *log.Logger) string {
	prompt := fmt.Sprintf("Email address [%s]: ", defaultEmail)
	errorFallback := defaultEmail
	if defaultEmail == "" {
		prompt = "Email address: "
		errorFallback = "email_query_failure@datawire.io"
	}

	for {
		fmt.Print(prompt)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		text := scanner.Text()
		if err := scanner.Err(); err != nil {
			log.Printf("Email query failed: %+v", err)
			return errorFallback
		}

		text = strings.TrimSpace(text)
		if defaultEmail != "" && text == "" {
			return defaultEmail
		}

		if validEmailAddress.MatchString(text) {
			return text
		}

		fmt.Printf("Sorry, %q does not match our email address filter.\n", text)
	}
}

func aesInstall(cmd *cobra.Command, args []string) error {
	verbose, _ := cmd.Flags().GetBool("verbose")
	kcontext, _ := cmd.Flags().GetString("context")
	i := NewInstaller(verbose)

	sup := supervisor.WithContext(i.ctx)
	sup.Logger = i.log

	sup.Supervise(&supervisor.Worker{
		Name: "signal",
		Work: func(p *supervisor.Process) error {
			sigs := make(chan os.Signal, 1)
			signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
			p.Ready()
			select {
			case sig := <-sigs:
				i.Report("user_interrupted", ScoutMeta{"signal", fmt.Sprintf("%+v", sig)})
				i.Quit()
			case <-p.Shutdown():
			}
			return nil
		},
	})
	sup.Supervise(&supervisor.Worker{
		Name:     "install",
		Requires: []string{"signal"},
		Work: func(p *supervisor.Process) error {
			defer i.Quit()
			return i.Perform(kcontext)
		},
	})

	runErrors := sup.Run()
	if len(runErrors) > 1 { // This shouldn't happen...
		for _, err := range runErrors {
			i.show.Printf(err.Error())
		}
	}
	if len(runErrors) > 0 {
		return runErrors[0]
	}
	return nil
}

func getManifest(url string) (string, error) {
	res, err := http.Get(url)
	if err != nil {
		return "", err
	}
	bodyBytes, err := ioutil.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return "", err
	}
	if res.StatusCode != 200 {
		return "", errors.Errorf("Bad status: %d", res.StatusCode)
	}
	return string(bodyBytes), nil
}

type LoopFailedError string

func (s LoopFailedError) Error() string {
	return string(s)
}

func (i *Installer) loopUntil(what string, how func() error) error {
	// Maybe these should be arguments?
	sleepTime := 500 * time.Millisecond // How long to sleep between calls
	progressTime := 15 * time.Second    // How long until we explain why we're waiting
	timeout := 120 * time.Second        // How long until we give up

	ctx, cancel := context.WithTimeout(i.ctx, timeout)
	defer cancel()
	start := time.Now()
	i.log.Printf("Waiting for %s", what)
	defer func() { i.log.Printf("Wait for %s took %.1f seconds", what, time.Since(start).Seconds()) }()
	progTimer := time.NewTimer(progressTime)
	defer progTimer.Stop()
	for {
		err := how()
		if err == nil {
			return nil // Success
		} else if _, ok := err.(LoopFailedError); ok {
			return err // Immediate failure
		}
		// Wait and try again
		select {
		case <-progTimer.C:
			i.show.Printf("(waiting for %s)", what)
		case <-time.After(sleepTime):
			// Try again
			// TODO: Fancy animated progress indicator?
		case <-ctx.Done():
			return errors.Errorf("timed out waiting for %s (or interrupted)", what)
		}
	}
}

// GrabAESInstallID uses "kubectl exec" to ask the AES pod for the cluster's ID,
// which we uses as the AES install ID. This has the side effect of making sure
// the Pod is Running (though not necessarily Ready). This should be good enough
// to report the "deploy" status to metrics.
func (i *Installer) GrabAESInstallID() error {
	// FIXME This doesn't work with `kubectl` 1.13 (and possibly 1.14). We
	// FIXME need to discover and use the pod name with `kubectl exec`.
	clusterID, err := i.CaptureKubectl("get cluster ID", "", "-n", "ambassador", "exec", "deploy/ambassador", "python3", "kubewatch.py")
	if err != nil {
		return err
	}
	i.SetMetadatum("Cluster ID", "aes_install_id", clusterID)
	return nil
}

func (i *Installer) GrabLoadBalancerIP() (string, error) {
	ipAddress, err := i.CaptureKubectl("get IP address", "", "get", "-n", "ambassador", "service", "ambassador", "-o", `go-template={{range .status.loadBalancer.ingress}}{{print .ip "\n"}}{{end}}`)
	if err != nil {
		return "", err
	}
	ipAddress = strings.TrimSpace(ipAddress)
	if ipAddress == "" {
		return "", errors.New("empty IP address")
	}
	return ipAddress, nil
}

func (i *Installer) CheckAESServesACME(ipAddress string) (err error) {
	defer func() {
		if err != nil {
			i.log.Print(err.Error())
		}
	}()

	// Verify that we can connect to something
	resp, err := http.Get("http://" + ipAddress + "/.well-known/acme-challenge/")
	if err != nil {
		err = errors.Wrap(err, "check for AES")
		return
	}
	_, _ = ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	// Verify that we get the expected status code. If Ambassador is still
	// starting up, then Envoy may return "upstream request timeout" (503),
	// in which case we should keep looping.
	if resp.StatusCode != 404 {
		err = errors.Errorf("check for AES: wrong status code: %d instead of 404", resp.StatusCode)
		return
	}

	// Sanity check that we're talking to Envoy. This is probably unnecessary.
	if resp.Header.Get("server") != "envoy" {
		err = errors.Errorf("check for AES: wrong server header: %s instead of envoy", resp.Header.Get("server"))
		return
	}

	return nil
}

func (i *Installer) CheckHostnameFound(hostname string) error {
	conn, err := net.Dial("tcp", fmt.Sprintf("check-%d.%s:443", time.Now().Unix(), hostname))
	if err == nil {
		conn.Close()
	}
	return err
}

func (i *Installer) CheckACMEIsDone(hostname string) error {
	state, err := i.CaptureKubectl("get Host state", "", "get", "host", hostname, "-o", "go-template={{.status.state}}")
	if err != nil {
		return LoopFailedError(err.Error())
	}
	if state == "Error" {
		return LoopFailedError(fmt.Sprintf("ACME failed. More information: kubectl get host %s -o yaml", hostname))
	}
	if state != "Ready" {
		return errors.Errorf("Host state is %s, not Ready", state)
	}
	return nil
}

func (i *Installer) Perform(kcontext string) error {
	// Start
	i.Report("install")

	// Download AES manifests
	crdManifests, err := getManifest("https://www.getambassador.io/yaml/aes-crds.yaml")
	if err != nil {
		i.Report("fail_no_internet", ScoutMeta{"err", err.Error()})
		return errors.Wrap(err, "download AES CRD manifests")
	}
	aesManifests, err := getManifest("https://www.getambassador.io/yaml/aes.yaml")
	if err != nil {
		i.Report("fail_no_internet", ScoutMeta{"err", err.Error()})
		return errors.Wrap(err, "download AES manifests")
	}

	// Figure out what version of AES is being installed
	// TODO: Parse the manifests and build objects
	// TODO: Extract version info from the Deployment object
	// TODO? Set label(s) on the to indicate this installation was performed by the installer
	aesVersionRE := regexp.MustCompile("image: quay[.]io/datawire/aes:([[:^space:]]+)[[:space:]]")
	matches := aesVersionRE.FindStringSubmatch(aesManifests)
	if len(matches) != 2 {
		i.log.Printf("matches is %+v", matches)
		return errors.Errorf("Failed to parse downloaded manifests. Is there a proxy server interfering with HTTP downloads?")
	}
	aesVersion := matches[1]
	i.SetMetadatum("AES version being installed", "aes_version", aesVersion)

	// Display version information
	i.show.Printf("-> Installing the Ambassador Edge Stack %s\n", aesVersion)

	// Attempt to talk to the specified cluster
	// TODO: Figure out cluster info to be passed along.
	// TODO: Cluster type (minikube or GKE or whatever): Look at node labels for
	//       the kubernetes.io/hostname label if possible; we can construct
	//       regular expressions to identify common cluster types.
	// TODO: Figure out if a previous installation exists
	// TODO: Grab Helm information if relevant. Look at the ambassador
	//       deployment's annotations for app.kubernetes.io/managed-by=Helm or
	//       similar. Figure out Helm version?

	i.kubeinfo = k8s.NewKubeInfo("", kcontext, "")
	if err := i.ShowKubectl("cluster-info", "", "cluster-info"); err != nil {
		i.Report("fail_no_cluster")
		return err
	}
	i.SetMetadatum("Cluster info", "cluster_info", "FIXME")

	// Install the AES manifests

	if err := i.ShowKubectl("install CRDs", crdManifests, "apply", "-f", "-"); err != nil {
		i.Report("fail_install_crds")
		return err
	}

	if err := i.ShowKubectl("wait for CRDs", "", "wait", "--for", "condition=established", "--timeout=90s", "crd", "-lproduct=aes"); err != nil {
		i.Report("fail_wait_crds")
		return err
	}

	if err := i.ShowKubectl("install AES", aesManifests, "apply", "-f", "-"); err != nil {
		i.Report("fail_install_aes")
		return err
	}

	if err := i.ShowKubectl("wait for AES", "", "-n", "ambassador", "wait", "--for", "condition=available", "--timeout=90s", "deploy", "-lproduct=aes"); err != nil {
		i.Report("fail_wait_aes")
		return err
	}

	// Wait for Ambassador Pod; grab AES install ID
	if err := i.loopUntil("AES pod startup", i.GrabAESInstallID); err != nil {
		i.Report("fail_pod_timeout")
		// TODO Is it possible to detect other errors? If so, report "fail_install_id", pass along the error
		return err
	}

	i.Report("deploy")

	// Grab load balancer IP address
	ipAddress := ""
	grabIP := func() error {
		var err error
		ipAddress, err = i.GrabLoadBalancerIP()
		return err
	}
	if err := i.loopUntil("Load Balancer", grabIP); err != nil {
		// FIXME this can fail in reasonable cases (Minikube, Kubernaut) and we don't handle that.
		return err
	}
	i.show.Println("Your IP address is", ipAddress)

	// Wait for Ambassador to be ready to serve ACME requests.
	// FIXME: This assumes we can connect to the load balancer. If this
	// assumption is incorrect, this code will loop forever.
	if err := i.loopUntil("AES to serve ACME", func() error { return i.CheckAESServesACME(ipAddress) }); err != nil {
		return err
	}

	i.show.Println("-> Automatically configuring TLS")

	// Attempt to grab a reasonable default for the user's email address
	defaultEmail, err := i.Capture("get email", "", "git", "config", "--global", "user.email")
	if err != nil {
		i.log.Print(err)
		defaultEmail = ""
	} else {
		defaultEmail = strings.TrimSpace(defaultEmail)
		if !validEmailAddress.MatchString(defaultEmail) {
			defaultEmail = ""
		}
	}

	// Ask for the user's email address
	i.show.Println(emailAsk)
	// Do the goroutine dance to let the user hit Ctrl-C at the email prompt
	gotEmail := make(chan (string))
	var emailAddress string
	go func() {
		gotEmail <- getEmailAddress(defaultEmail, i.log)
		close(gotEmail)
	}()
	select {
	case emailAddress = <-gotEmail:
		// Continue
	case <-i.ctx.Done():
		fmt.Println()
		return errors.New("Interrupted")
	}

	i.log.Printf("Using email address %q", emailAddress)

	// Did the user hit Ctrl-C at the email prompt?

	// Send a request to acquire a DNS name for this cluster's IP address
	regURL := "https://metriton.datawire.io/beta/register-domain"
	buf := new(bytes.Buffer)
	_ = json.NewEncoder(buf).Encode(registration{emailAddress, ipAddress})
	resp, err := http.Post(regURL, "application/json", buf)
	if err != nil {
		return errors.Wrap(err, "acquire DNS name (post)")
	}
	content, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return errors.Wrap(err, "acquire DNS name (read body)")
	}

	if resp.StatusCode == 200 {
		hostname := string(content)
		i.show.Println("-> Acquiring DNS name", hostname)

		// Wait for DNS to propagate. This tries to avoid waiting for a ten
		// minute error backoff if the ACME registration races ahead of the DNS
		// name appearing for LetsEncrypt.
		if err := i.loopUntil("DNS propagation to this host", func() error { return i.CheckHostnameFound(hostname) }); err != nil {
			return err
		}

		// Create a Host resource
		hostResource := fmt.Sprintf(hostManifest, hostname, hostname, emailAddress)
		if err := i.ShowKubectl("install Host resource", hostResource, "apply", "-f", "-"); err != nil {
			return err
		}

		i.show.Println("-> Obtaining a TLS certificate from Let's Encrypt")
		if err := i.loopUntil("TLS certificate acquisition", func() error { return i.CheckACMEIsDone(hostname) }); err != nil {
			return err
		}

		i.Report("cert_provisioned")
		i.show.Println("-> TLS configured successfully")
		if err := i.ShowKubectl("show Host", "", "get", "host", hostname); err != nil {
			return err
		}

		// Open a browser window to the Edge Policy Console
		if err := do_login(i.kubeinfo, kcontext, "ambassador", hostname, false, false); err != nil {
			return err
		}

	} else {
		message := strings.TrimSpace(string(content))
		i.show.Println("-> Failed to create a DNS name:", message)
		i.show.Println()
		i.show.Println("If this IP address is reachable from here, then the following command")
		i.show.Println("will open the Edge Policy Console once you accept a self-signed")
		i.show.Println("certificate in your browser.")
		i.show.Println()
		i.show.Println("    edgectl login -n ambassador", ipAddress)
		i.show.Println()
		i.show.Println("If the IP is not reachable from here, you can use port forwarding to")
		i.show.Println("access the Edge Policy Console.")
		i.show.Println()
		i.show.Println("    kubectl -n ambassador port-forward deploy/ambassador 8443 &")
		i.show.Println("    edgectl login -n ambassador 127.0.0.1:8443")
		i.show.Println()
		i.show.Println("You will need to accept a self-signed certificate in your browser.")
		i.show.Println()
		i.show.Println("See https://www.getambassador.io/user-guide/getting-started/")
	}

	// TODO: Report metric "aes_health_good" when AES is reachable and healthy:
	// hit /ambassador/v0/check_ready and see if you get a 200. Otherwise report
	// metric "aes_health_bad"
	i.Report("aes_health_good")

	return nil
}

type Installer struct {
	kubeinfo *k8s.KubeInfo
	scout    *Scout
	ctx      context.Context
	cancel   context.CancelFunc
	show     *log.Logger
	log      *log.Logger
	cmdOut   *log.Logger
	cmdErr   *log.Logger
	logName  string
}

func NewInstaller(verbose bool) *Installer {
	// Although log, cmdOut, and cmdErr *can* go to different files and/or have
	// different prefixes, they'll probably all go to the same file, possibly
	// with different prefixes, for most cases.
	logfileName := filepath.Join(os.TempDir(), time.Now().Format("edgectl-install-20060102-150405.log"))
	logfile, err := os.Create(logfileName)
	if err != nil {
		logfile = os.Stderr
		fmt.Fprintf(logfile, "WARNING: Failed to open logfile %q: %+v\n", logfileName, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	i := &Installer{
		scout:   NewScout("install"),
		ctx:     ctx,
		cancel:  cancel,
		show:    log.New(io.MultiWriter(os.Stdout, logfile), "", 0),
		logName: logfileName,
	}
	if verbose {
		i.log = log.New(io.MultiWriter(logfile, NewLoggingWriter(log.New(os.Stderr, "== ", 0))), "", log.Ltime)
		i.cmdOut = log.New(io.MultiWriter(logfile, NewLoggingWriter(log.New(os.Stderr, "=- ", 0))), "", 0)
		i.cmdErr = log.New(io.MultiWriter(logfile, NewLoggingWriter(log.New(os.Stderr, "=x ", 0))), "", 0)
	} else {
		i.log = log.New(logfile, "", log.Ltime)
		i.cmdOut = log.New(logfile, "", 0)
		i.cmdErr = log.New(logfile, "", 0)
	}

	return i
}

func (i *Installer) Quit() {
	i.cancel()
}

// Kubernetes Cluster

func (i *Installer) ShowKubectl(name string, input string, args ...string) error {
	kargs, err := i.kubeinfo.GetKubectlArray(args...)
	if err != nil {
		return errors.Wrapf(err, "cluster access for %s", name)
	}
	i.log.Printf("$ kubectl %s", strings.Join(kargs, " "))
	cmd := exec.Command("kubectl", kargs...)
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout = NewLoggingWriter(i.cmdOut)
	cmd.Stderr = NewLoggingWriter(i.cmdErr)
	if err := cmd.Run(); err != nil {
		return errors.Wrap(err, name)
	}
	return nil
}

func (i *Installer) CaptureKubectl(name, input string, args ...string) (res string, err error) {
	res = ""
	kargs, err := i.kubeinfo.GetKubectlArray(args...)
	if err != nil {
		err = errors.Wrapf(err, "cluster access for %s", name)
		return
	}
	kargs = append([]string{"kubectl"}, kargs...)
	return i.Capture(name, input, kargs...)
}

func (i *Installer) Capture(name, input string, args ...string) (res string, err error) {
	res = ""
	resAsBytes := &bytes.Buffer{}
	i.log.Printf("$ %s", strings.Join(args, " "))
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout = io.MultiWriter(NewLoggingWriter(i.cmdOut), resAsBytes)
	cmd.Stderr = NewLoggingWriter(i.cmdErr)
	err = cmd.Run()
	if err != nil {
		err = errors.Wrap(err, name)
	}
	res = resAsBytes.String()
	return
}

// Metrics

func (i *Installer) SetMetadatum(name, key string, value interface{}) {
	i.log.Printf("[Metrics] %s (%q) is %q", name, key, value)
	i.scout.SetMetadatum(key, value)
}

func (i *Installer) Report(eventName string, meta ...ScoutMeta) {
	i.log.Println("[Metrics]", eventName)
	if err := i.scout.Report(eventName, meta...); err != nil {
		i.log.Println("[Metrics]", eventName, err)
	}
}

// registration is used to register edgestack.me domains
type registration struct {
	Email string
	Ip    string
}

const hostManifest = `
apiVersion: getambassador.io/v2
kind: Host
metadata:
  name: %s
spec:
  hostname: %s
  acmeProvider:
    email: %s
`

// FIXME: Mention that this will be shared with Let's Encrypt?
const emailAsk = `Please enter an email address. We'll use this email address to notify you
prior to domain and certificate expiration.`

var validEmailAddress = regexp.MustCompile("^[a-zA-Z0-9.!#$%&'*+/=?^_`{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$")
