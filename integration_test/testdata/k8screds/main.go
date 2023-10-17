package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"

	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/pkg/apis/clientauthentication"
	"k8s.io/client-go/pkg/apis/clientauthentication/install"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
)

func main() {
	if err := run(os.Args); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	var cn string
	var fm map[string]string
	switch len(args) {
	case 1:
	case 2:
		cn = os.Args[1]
		fm = map[string]string{"context": cn}
	default:
		return fmt.Errorf("usage %s <name of kubecontext>", args)
	}
	flags, err := client.ConfigFlags(fm)
	if err != nil {
		return err
	}
	config, err := flags.ToRawKubeConfigLoader().RawConfig()
	if err != nil {
		return err
	}
	if cn == "" {
		cn = config.CurrentContext
	} else {
		config.CurrentContext = cn
	}
	if err = api.MinifyConfig(&config); err != nil {
		return fmt.Errorf("unable to load context %q: %w", cn, err)
	}
	// Ensure that all certs are embedded instead of reachable using a path
	if err = api.FlattenConfig(&config); err != nil {
		return fmt.Errorf("unable to flatten context %q: %w", cn, err)
	}
	cc := config.Contexts[cn]
	ai, ok := config.AuthInfos[cc.AuthInfo]
	if !ok {
		return fmt.Errorf("unable to load authinfo %q for context %q", cc.AuthInfo, cn)
	}

	var data []byte
	if ec := ai.Exec; ec != nil {
		data, err = resolveExec(ec)
	} else {
		data, err = resolveCreds(ai, config.Clusters[cc.Cluster])
	}
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

func resolveCreds(ai *api.AuthInfo, cl *api.Cluster) ([]byte, error) {
	st := clientauthentication.ExecCredentialStatus{
		Token: ai.Token,
	}
	if len(ai.ClientCertificateData) > 0 {
		st.ClientCertificateData = string(ai.ClientCertificateData)
	}
	if len(ai.ClientCertificateData) > 0 {
		st.ClientKeyData = string(ai.ClientKeyData)
	}
	creds := clientauthentication.ExecCredential{
		TypeMeta: meta.TypeMeta{
			Kind:       "ExecCredential",
			APIVersion: "client.authentication.k8s.io/v1beta1",
		},
		Spec: clientauthentication.ExecCredentialSpec{
			Interactive: false,
		},
		Status: &st,
	}
	if cl != nil {
		creds.Spec.Cluster = &clientauthentication.Cluster{
			Server:                   cl.Server,
			TLSServerName:            cl.TLSServerName,
			InsecureSkipTLSVerify:    cl.InsecureSkipTLSVerify,
			CertificateAuthorityData: cl.CertificateAuthorityData,
			ProxyURL:                 cl.ProxyURL,
			DisableCompression:       cl.DisableCompression,
		}
	}
	scheme := runtime.NewScheme()
	install.Install(scheme)
	codecs := serializer.NewCodecFactory(scheme)
	return runtime.Encode(codecs.LegacyCodec(creds.GroupVersionKind().GroupVersion()), &creds)
}

func resolveExec(execConfig *api.ExecConfig) ([]byte, error) {
	var buf bytes.Buffer
	cmd := exec.Command(execConfig.Command, execConfig.Args...)
	cmd.Stdout = &buf
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if len(execConfig.Env) > 0 {
		em := dos.FromEnvPairs(cmd.Env)
		for _, ev := range execConfig.Env {
			em[ev.Name] = ev.Value
		}
		cmd.Env = em.Environ()
	}

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to run host command: %w", err)
	}

	return buf.Bytes(), nil
}
