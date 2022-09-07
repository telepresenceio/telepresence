package cli

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

type genYAMLInfo struct {
	outputFile   string
	inputFile    string
	configFile   string
	workloadName string
	namespace    string
}

func genYAMLCommand() *cobra.Command {
	info := genYAMLInfo{}
	cmd := &cobra.Command{
		Use:  "genyaml",
		Args: cobra.NoArgs,

		Short: "Generate YAML for use in kubernetes manifests.",
		Long: `Generate traffic-agent yaml for use in kubernetes manifests.
This allows the traffic agent to be injected by hand into existing kubernetes manifests.
For your modified workload to be valid, you'll have to manually inject a container and a
volume into the workload, and a corresponding configmap entry into the "telelepresence-agents"
configmap; you can do this by running "genyaml config", "genyaml container", and "genyaml volume".

NOTE: It is recommended that you not do this unless strictly necessary. Instead, we suggest letting
telepresence's webhook injector configure the traffic agents on demand.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return errcat.User.New("please run genyaml as \"genyaml config\", \"genyaml container\", \"genyaml initcontainer\", or \"genyaml volume\"")
		},
	}
	flags := cmd.PersistentFlags()
	flags.StringVarP(&info.outputFile, "output", "o", "-",
		"Path to the file to place the output in. Defaults to '-' which means stdout.")
	cmd.AddCommand(
		genConfigMapSubCommand(&info),
		genContainerSubCommand(&info),
		genInitContainerSubCommand(&info),
		genVolumeSubCommand(&info),
	)
	return cmd
}

func getInput(inputFile string) ([]byte, error) {
	var f io.ReadCloser
	if inputFile == "-" {
		f = os.Stdin
	} else {
		var err error
		if f, err = os.Open(inputFile); err != nil {
			return nil, errcat.User.Newf("unable to open input file %q: %w", inputFile, err)
		}
		defer f.Close()
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, errcat.User.Newf("error reading from %s: %w", inputFile, err)
	}
	return b, nil
}

func (i *genYAMLInfo) getOutputWriter() (io.WriteCloser, error) {
	if i.outputFile == "-" {
		return os.Stdout, nil
	}
	f, err := os.Create(i.outputFile)
	if err != nil {
		return nil, errcat.User.Newf("unable to open output file %s: %w", i.outputFile, err)
	}
	return f, nil
}

func (i *genYAMLInfo) loadConfigMapEntry(ctx context.Context) (*agentconfig.Sidecar, error) {
	if i.configFile != "" {
		b, err := getInput(i.configFile)
		if err != nil {
			return nil, err
		}
		var cfg agentconfig.Sidecar
		if err = yaml.Unmarshal(b, &cfg); err != nil {
			return nil, errcat.User.Newf("unable to parse config %s: %w", i.configFile, err)
		}
		return &cfg, nil
	}
	if i.workloadName == "" {
		return nil, errcat.User.New("either --config or --workload must be provided")
	}

	// Load configmap entry from the telepresence-agents configmap
	cm, err := k8sapi.GetK8sInterface(ctx).CoreV1().ConfigMaps(i.namespace).Get(ctx, agentconfig.ConfigMap, meta.GetOptions{})
	if err != nil {
		return nil, errcat.User.New(err)
	}
	var yml string
	ok := false
	if cm.Data != nil {
		yml, ok = cm.Data[i.workloadName]
	}
	if !ok {
		return nil, errcat.User.Newf("Unable to load entry for %q in configmap %q: %w", i.workloadName, agentconfig.ConfigMap, err)
	}
	var cfg agentconfig.Sidecar
	if err = yaml.Unmarshal([]byte(yml), &cfg); err != nil {
		return nil, errcat.User.Newf("Unable to parse entry for %q in configmap %q: %w", i.workloadName, agentconfig.ConfigMap, err)
	}
	return &cfg, nil
}

func (i *genYAMLInfo) loadWorkload(ctx context.Context) (k8sapi.Workload, error) {
	if i.inputFile == "" {
		if i.workloadName == "" {
			return nil, errcat.User.New("either --input or --workload must be provided")
		}
		return k8sapi.GetWorkload(ctx, i.workloadName, i.namespace, "")
	}
	b, err := getInput(i.inputFile)
	if err != nil {
		return nil, err
	}

	scheme := runtime.NewScheme()
	scheme.AddKnownTypes(schema.GroupVersion{Group: apps.GroupName, Version: "v1"}, &apps.StatefulSet{}, &apps.Deployment{}, &apps.ReplicaSet{})
	codecFactory := serializer.NewCodecFactory(scheme)
	deserializer := codecFactory.UniversalDeserializer()

	obj, kind, err := deserializer.Decode(b, nil, nil)
	if err != nil {
		return nil, errcat.User.Newf("unable to parse yaml in %s: %w", i.inputFile, err)
	}
	wl, err := k8sapi.WrapWorkload(obj)
	if err != nil {
		return nil, errcat.User.Newf("unexpected object of kind %s; please pass in a Deployment, ReplicaSet, or StatefulSet", kind)
	}
	if wl.GetNamespace() == "" {
		if d, ok := k8sapi.DeploymentImpl(wl); ok {
			d.Namespace = i.namespace
		} else if r, ok := k8sapi.ReplicaSetImpl(wl); ok {
			r.Namespace = i.namespace
		} else if s, ok := k8sapi.StatefulSetImpl(wl); ok {
			s.Namespace = i.namespace
		}
	}
	return wl, nil
}

func (i *genYAMLInfo) writeObjToOutput(obj any) error {
	// We use sigs.ks8.io/yaml because it treats json serialization tags as if they were yaml tags.
	doc, err := yaml.Marshal(obj)
	if err != nil {
		return errcat.User.Newf("unable to marshal agent container: %w", err)
	}
	w, err := i.getOutputWriter()
	if err != nil {
		return err
	}
	defer w.Close()
	_, err = w.Write(doc)
	if err != nil {
		return errcat.User.Newf("unable to write to output %s: %w", i.outputFile, err)
	}
	return nil
}

func (i *genYAMLInfo) withK8sInterface(ctx context.Context, flagMap map[string]string) (context.Context, error) {
	configFlags := genericclioptions.NewConfigFlags(false)
	flags := pflag.NewFlagSet("", 0)
	configFlags.AddFlags(flags)
	for k, v := range flagMap {
		if err := flags.Set(k, v); err != nil {
			return nil, errcat.User.Newf("error processing kubectl flag --%s=%s: %w", k, v, err)
		}
	}

	configLoader := configFlags.ToRawKubeConfigLoader()
	restConfig, err := configLoader.ClientConfig()
	if err != nil {
		return nil, errcat.Config.New(err)
	}

	config, err := configLoader.RawConfig()
	if err != nil {
		return nil, errcat.Config.New(err)
	}
	if len(config.Contexts) == 0 {
		return nil, errcat.Config.New("kubeconfig has no context definition")
	}

	ctxName := flagMap["context"]
	if ctxName == "" {
		ctxName = config.CurrentContext
	}
	c, ok := config.Contexts[ctxName]
	if !ok {
		return nil, errcat.Config.Newf("context %q does not exist in the kubeconfig", ctxName)
	}
	i.namespace = c.Namespace
	if i.namespace == "" {
		i.namespace = flagMap["namespace"]
		if i.namespace == "" {
			i.namespace = "default"
		}
	}
	cs, err := kubernetes.NewForConfig(restConfig)
	if err == nil {
		ctx = k8sapi.WithK8sInterface(ctx, cs)
	}
	return ctx, err
}

type genConfigMap struct {
	agentmap.GeneratorConfig
	*genYAMLInfo
}

func allKubeFlags() *pflag.FlagSet {
	kubeFlags := pflag.NewFlagSet("Kubernetes flags", 0)
	kubeConfig := genericclioptions.NewConfigFlags(false)
	kubeConfig.AddFlags(kubeFlags)
	return kubeFlags
}

func genConfigMapSubCommand(yamlInfo *genYAMLInfo) *cobra.Command {
	kubeFlags := allKubeFlags()
	info := genConfigMap{genYAMLInfo: yamlInfo}
	cmd := &cobra.Command{
		Use:   "config",
		Args:  cobra.NoArgs,
		Short: "Generate YAML for the agent's entry in the telepresence-agents configmap.",
		Long:  "Generate YAML for the agent's entry in the telepresence-agents configmap. See genyaml for more info on what this means",
		RunE: func(cmd *cobra.Command, args []string) error {
			return info.run(cmd, kubeFlagMap(kubeFlags))
		},
	}
	flags := cmd.Flags()
	flags.StringVarP(&info.inputFile, "input", "i", "",
		"Path to the yaml containing the workload definition (i.e. Deployment, StatefulSet, etc). Pass '-' for stdin.. Mutually exclusive to --workload")
	flags.StringVarP(&info.workloadName, "workload", "w", "",
		"Name of the workload. If given, the workload will be retrieved from the cluster, mutually exclusive to --input")
	flags.Uint16Var(&info.AgentPort, "agent-port", 9900,
		"The port number you wish the agent to listen on.")
	flags.StringVar(&info.QualifiedAgentImage, "agent-image", "docker.io/datawire/tel2:"+strings.TrimPrefix(client.Version(), "v"),
		`The qualified name of the agent image`)
	flags.Uint16Var(&info.ManagerPort, "manager-port", 8081,
		`The traffic-manager API port`)
	flags.StringVar(&info.ManagerNamespace, "manager-namespace", "ambassador",
		`The traffic-manager namespace`)
	flags.StringVar(&info.LogLevel, "loglevel", "info",
		`The loglevel for the generated traffic-agent sidecar`)
	flags.AddFlagSet(kubeFlags)
	return cmd
}

func (i *genConfigMap) generateConfigMap(ctx context.Context, wl k8sapi.Workload) (*agentconfig.Sidecar, error) {
	ac, err := agentmap.Generate(ctx, wl, &i.GeneratorConfig)
	if err != nil {
		return nil, errcat.NoDaemonLogs.New(err)
	}
	return ac, nil
}

func (g *genConfigMap) run(cmd *cobra.Command, kubeFlags map[string]string) error {
	ctx, err := g.withK8sInterface(cmd.Context(), kubeFlags)
	if err != nil {
		return err
	}

	wl, err := g.loadWorkload(ctx)
	if err != nil {
		return err
	}

	cfg, err := g.generateConfigMap(ctx, wl)
	if err != nil {
		return err
	}
	cfg.Manual = true
	return g.writeObjToOutput(cfg)
}

type genContainerInfo struct {
	*genYAMLInfo
}

func genContainerSubCommand(yamlInfo *genYAMLInfo) *cobra.Command {
	kubeFlags := allKubeFlags()
	info := genContainerInfo{genYAMLInfo: yamlInfo}
	cmd := &cobra.Command{
		Use:   "container",
		Args:  cobra.NoArgs,
		Short: "Generate YAML for the traffic-agent container.",
		Long:  "Generate YAML for the traffic-agent container. See genyaml for more info on what this means",
		RunE: func(cmd *cobra.Command, args []string) error {
			return info.run(cmd, kubeFlagMap(kubeFlags))
		},
	}
	flags := cmd.Flags()
	flags.StringVarP(&info.inputFile, "input", "i", "",
		"Optional path to the yaml containing the workload definition (i.e. Deployment, StatefulSet, etc). Pass '-' for stdin. Loaded from cluster by default")
	flags.StringVarP(&info.workloadName, "workload", "w", "",
		"Name of the workload. If given, the configmap entry will be retrieved telepresence-agents configmap, mutually exclusive to --config")
	flags.StringVarP(&info.configFile, "config", "c", "", "Path to the yaml containing the generated configmap entry, mutually exclusive to --workload")
	flags.AddFlagSet(kubeFlags)
	return cmd
}

func (g *genContainerInfo) run(cmd *cobra.Command, kubeFlags map[string]string) error {
	ctx, err := g.withK8sInterface(cmd.Context(), kubeFlags)
	if err != nil {
		return err
	}

	cm, err := g.loadConfigMapEntry(ctx)
	if err != nil {
		return err
	}
	if g.inputFile == "" {
		g.workloadName = cm.WorkloadName
	}

	wl, err := g.loadWorkload(ctx)
	if err != nil {
		return err
	}

	// Sanity check
	if wl.GetName() != cm.WorkloadName {
		return errcat.User.Newf("name %q of loaded workload is different from %q loaded configmap entry", wl.GetName(), cm.WorkloadName)
	}
	if wl.GetKind() != cm.WorkloadKind {
		return errcat.User.Newf("kind %q of loaded workload is different from %q loaded configmap entry", wl.GetKind(), cm.WorkloadKind)
	}

	podTpl := wl.GetPodTemplate()
	agentContainer := agentconfig.AgentContainer(
		ctx,
		&core.Pod{
			TypeMeta: meta.TypeMeta{
				Kind:       "pod",
				APIVersion: "v1",
			},
			ObjectMeta: podTpl.ObjectMeta,
			Spec:       podTpl.Spec,
		},
		cm,
	)
	return g.writeObjToOutput(agentContainer)
}

type genInitContainerInfo struct {
	*genYAMLInfo
}

func genInitContainerSubCommand(yamlInfo *genYAMLInfo) *cobra.Command {
	kubeFlags := allKubeFlags()
	info := genInitContainerInfo{genYAMLInfo: yamlInfo}
	cmd := &cobra.Command{
		Use:   "initcontainer",
		Args:  cobra.NoArgs,
		Short: "Generate YAML for the traffic-agent init container.",
		Long:  "Generate YAML for the traffic-agent init container. See genyaml for more info on what this means",
		RunE: func(cmd *cobra.Command, args []string) error {
			return info.run(cmd, kubeFlagMap(kubeFlags))
		},
	}
	flags := cmd.Flags()
	flags.StringVarP(&info.workloadName, "workload", "w", "",
		"Name of the workload. If given, the configmap entry will be retrieved telepresence-agents configmap, mutually exclusive to --config")
	flags.StringVarP(&info.configFile, "config", "c", "", "Path to the yaml containing the generated configmap entry, mutually exclusive to --workload")
	flags.AddFlagSet(kubeFlags)
	return cmd
}

func (g *genInitContainerInfo) run(cmd *cobra.Command, kubeFlags map[string]string) error {
	ctx, err := g.withK8sInterface(cmd.Context(), kubeFlags)
	if err != nil {
		return err
	}

	cm, err := g.loadConfigMapEntry(ctx)
	if err != nil {
		return err
	}

	for _, cc := range cm.Containers {
		for _, ic := range cc.Intercepts {
			if ic.Headless || ic.TargetPortNumeric {
				return g.writeObjToOutput(agentconfig.InitContainer(cm))
			}
		}
	}
	return errcat.User.New("deployment does not need an init container")
}

type genVolumeInfo struct {
	*genYAMLInfo
}

func genVolumeSubCommand(yamlInfo *genYAMLInfo) *cobra.Command {
	info := genVolumeInfo{genYAMLInfo: yamlInfo}
	kubeFlags := allKubeFlags()
	cmd := &cobra.Command{
		Use:   "volume",
		Args:  cobra.NoArgs,
		Short: "Generate YAML for the traffic-agent volume.",
		Long:  "Generate YAML for the traffic-agent volume. See genyaml for more info on what this means",
		RunE: func(cmd *cobra.Command, args []string) error {
			return info.run(cmd, kubeFlagMap(kubeFlags))
		},
	}
	flags := cmd.Flags()
	flags.StringVarP(&info.workloadName, "workload", "w", "", "Name of the workload.")
	return cmd
}

func (g *genVolumeInfo) run(cmd *cobra.Command, kubeFlags map[string]string) error {
	if g.workloadName == "" {
		return errcat.User.New("missing required flag --workload")
	}
	ctx, err := g.withK8sInterface(cmd.Context(), kubeFlags)
	if err != nil {
		return err
	}

	cm, err := g.loadConfigMapEntry(ctx)
	if err != nil {
		return err
	}
	if g.inputFile == "" {
		g.workloadName = cm.WorkloadName
	}

	wl, err := g.loadWorkload(ctx)
	if err != nil {
		return err
	}

	// Sanity check
	if wl.GetName() != cm.WorkloadName {
		return errcat.User.Newf("name %q of loaded workload is different from %q loaded configmap entry", wl.GetName(), cm.WorkloadName)
	}
	if wl.GetKind() != cm.WorkloadKind {
		return errcat.User.Newf("kind %q of loaded workload is different from %q loaded configmap entry", wl.GetKind(), cm.WorkloadKind)
	}

	podTpl := wl.GetPodTemplate()

	volumes := agentconfig.AgentVolumes(g.workloadName, &core.Pod{
		TypeMeta: meta.TypeMeta{
			Kind:       "pod",
			APIVersion: "v1",
		},
		ObjectMeta: podTpl.ObjectMeta,
		Spec:       podTpl.Spec,
	})

	return g.writeObjToOutput(&volumes)
}
