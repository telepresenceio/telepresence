package cli

import (
	"archive/zip"
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

type gatherLogsArgs struct {
	outputFile     string
	daemons        string
	trafficAgents  string
	trafficManager bool
	anon           bool
}

func gatherLogsCommand() *cobra.Command {
	gl := &gatherLogsArgs{}
	cmd := &cobra.Command{
		Use:   "gather-logs",
		Args:  cobra.NoArgs,
		Short: "Gather logs from traffic-manager, traffic-agent, user and root daemons, and export them into a zip file.",
		Long: `Gather logs from traffic-manager, traffic-agent, user and root daemons,
and export them into a zip file. Useful if you are opening a Github issue or asking
someone to help you debug Telepresence.`,
		Example: `Here are a few examples of how you can use this command:
# Get all logs and export to a given file
telepresence gather-logs -o /tmp/telepresence_logs.zip

# Get all logs for the daemons only
telepresence gather-logs --traffic-agents=None --traffic-manager=False

# Get all logs for pods that have "echo-easy" in the name, useful if you have multiple replicas
telepresence gather-logs --traffic-manager=False --traffic-agents=echo-easy

# Get all logs for a specific pod
telepresence gather-logs --traffic-manager=False --traffic-agents=echo-easy-6848967857-tw4jw     

# Get logs from everything except the daemons
telepresence gather-logs --daemons=None
`,

		RunE: func(cmd *cobra.Command, _ []string) error {
			return gl.gatherLogs(cmd.Context(), cmd, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	flags := cmd.Flags()
	flags.StringVarP(&gl.outputFile, "output-file", "o", "", "The file you want to output the logs to.")
	flags.StringVar(&gl.daemons, "daemons", "all", "The daemons you want logs from: all, root, user, None")
	flags.BoolVar(&gl.trafficManager, "traffic-manager", true, "If you want to collect logs from the traffic-manager")
	flags.StringVar(&gl.trafficAgents, "traffic-agents", "all", "Traffic-agents to collect logs from: all, name substring, None")
	flags.BoolVarP(&gl.anon, "anonymize", "a", false, "To anonymize pod names + namespaces from the logs")
	return cmd
}

// anonymizer contains the mappings between things we want to anonymize
// and their new, anonymized name.  Using a map instead of simply redacting
// makes it easier for us to maintain certain relationships in the logs (e.g.
// namespaces things are in) which may be helpful in troubleshooting.
type anonymizer struct {
	namespaces map[string]string
	podNames   map[string]string
}

// gatherLogs gets the logs from the daemons (daemon + connector) and creates a zip
func (gl *gatherLogsArgs) gatherLogs(ctx context.Context, cmd *cobra.Command, stdout, stderr io.Writer) error {
	scout := scout.NewScout(ctx, "cli")
	// Get the log directory and return the error if we can't get it
	logDir, err := filelocation.AppUserLogDir(ctx)
	if err != nil {
		return errcat.User.New(err)
	}

	anonymizer := &anonymizer{
		namespaces: make(map[string]string),
		podNames:   make(map[string]string),
	}
	// If the user did not provide an outputFile, we'll use their current working directory
	if gl.outputFile == "" {
		pwd, err := os.Getwd()
		if err != nil {
			return errcat.User.New(err)
		}
		gl.outputFile = fmt.Sprintf("%s/telepresence_logs.zip", pwd)
	} else if !strings.HasSuffix(gl.outputFile, ".zip") {
		return errcat.User.New("output file must end in .zip")
	}

	// Create a temporary directory where we will store the logs before we zip
	// them for export
	exportDir, err := os.MkdirTemp("", "logexp-")
	if err != nil {
		return errcat.User.New(err)
	}
	defer func() {
		if err := os.RemoveAll(exportDir); err != nil {
			fmt.Fprintf(stderr, "Failed to remove temp directory %s: %s", exportDir, err)
		}
	}()

	// First we add the daemonLogs to the export directory
	var daemonLogs []string
	switch gl.daemons {
	case "all":
		daemonLogs = append(daemonLogs, "connector", "daemon")
	case "root":
		daemonLogs = append(daemonLogs, "daemon")
	case "user":
		daemonLogs = append(daemonLogs, "connector")
	case "None":
	default:
		return errcat.User.New("Options for --daemons are: all, root, user, or None")
	}
	// Add metadata about the request so we can track usage + see which
	// types of logs people are requesting more frequently.
	// This also gives us an idea about how much usage this command is
	// getting.
	scout.SetMetadatum("daemon_logs", daemonLogs)
	scout.SetMetadatum("traffic_manager_logs", gl.trafficManager)
	scout.SetMetadatum("traffic_agent_logs", gl.trafficAgents)
	scout.Report(log.WithDiscardingLogger(ctx), "used_gather_logs")

	// Get all logs from the logdir that match the daemons the user cares about.
	logFiles, err := os.ReadDir(logDir)
	if err != nil {
		return errcat.User.New(err)
	}
	for _, entry := range logFiles {
		if entry.IsDir() {
			continue
		}
		for _, logType := range daemonLogs {
			if strings.Contains(entry.Name(), logType) {
				srcFile := fmt.Sprintf("%s/%s", logDir, entry.Name())
				dstFile := fmt.Sprintf("%s/%s", exportDir, entry.Name())
				if err := copyFiles(dstFile, srcFile); err != nil {
					// We don't want to fail / exit abruptly if we can't copy certain
					// files, but we do want the user to know we were unsuccessful
					fmt.Fprintf(stderr, "failed exporting %s: %s\n", entry.Name(), err)
					continue
				}
			}
		}
	}

	// Since getting the logs from k8s requires the connector, let's only do this
	// work if we know the user wants to get logs from k8s.
	if gl.trafficManager || gl.trafficAgents != "None" {
		// To get logs from the components in the kubernetes cluster, we ask the
		// traffic-manager.
		rq := &manager.GetLogsRequest{
			TrafficManager: gl.trafficManager,
			Agents:         gl.trafficAgents,
		}
		err = withConnector(cmd, false, func(ctx context.Context, connectorClient connector.ConnectorClient, connInfo *connector.ConnectInfo) error {
			err = cliutil.WithManager(ctx, func(ctx context.Context, managerClient manager.ManagerClient) error {
				lr, err := managerClient.GetLogs(ctx, rq)
				if err != nil {
					return err
				}
				// Write the logs for each pod to files
				for podName, log := range lr.PodLogs {
					podName = getPodName(podName, gl.anon, anonymizer)
					agentLogFile := fmt.Sprintf("%s/%s.log", exportDir, podName)
					fd, err := os.Create(agentLogFile)
					if err != nil {
						return err
					}
					defer fd.Close()
					fdWriter := bufio.NewWriter(fd)
					_, _ = fdWriter.WriteString(log)
					fdWriter.Flush()
				}
				return nil
			})
			if err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			return errcat.Unknown.Newf("error getting logs from kubernetes components: %s", err)
		}
	}

	// Now we anonymize the logs
	// Zip up all the files we've created in the zip directory and return that to the user
	var files []string
	dirEntries, err := os.ReadDir(exportDir)
	if err != nil {
		return errcat.User.New(err)
	}
	for _, entry := range dirEntries {
		if entry.IsDir() {
			continue
		}

		fullFileName := fmt.Sprintf("%s/%s", exportDir, entry.Name())
		// anonymize the log if necessary
		if gl.anon {
			anonymizeLog(stdout, fullFileName, anonymizer)
		}
		files = append(files, fullFileName)
	}

	if err := zipFiles(files, gl.outputFile); err != nil {
		return errcat.User.New(err)
	}

	fmt.Fprintf(stdout, "Logs have been exported to %s\n", gl.outputFile)
	return nil
}

// copyFiles copies files from one location into another.
func copyFiles(dstFile, srcFile string) error {
	srcWriter, err := os.Open(srcFile)
	if err != nil {
		return err
	}
	defer srcWriter.Close()

	dstWriter, err := os.Create(dstFile)
	if err != nil {
		return err
	}
	defer dstWriter.Close()

	if _, err := io.Copy(dstWriter, srcWriter); err != nil {
		return err
	}
	return nil
}

// zipFiles creates a zip file with the contents of all the files passed in.
// If some of the files do not exist, it will include that in the error message
// but it will still create a zip file with as many files as it can.
func zipFiles(files []string, zipFileName string) error {
	zipFile, err := os.Create(zipFileName)
	if err != nil {
		return err
	}
	defer zipFile.Close()

	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()

	addFileToZip := func(file string) error {
		fd, err := os.Open(file)
		if err != nil {
			return err
		}
		defer fd.Close()

		// Get the basename of the file since that's all we want
		// to include in the zip
		baseName := filepath.Base(file)
		zfd, err := zipWriter.Create(baseName)
		if err != nil {
			return err
		}
		if _, err := io.Copy(zfd, fd); err != nil {
			return err
		}
		return nil
	}

	// Make a note of the files we fail to add to the zip so users know if the
	// zip is incomplete
	errMsg := ""
	for _, file := range files {
		if err := addFileToZip(file); err != nil {
			errMsg += fmt.Sprintf("failed adding %s to zip file: %s", file, err)
		}
	}
	if errMsg != "" {
		return errors.New(errMsg)
	}
	return nil
}

// getPodName either returns the podName passed in or gets the anonymized
// name of the pod.  If the podName has not been yet anonymized in the
// anonymizer, then it will create the anonymized name and store it in
// the anonymizer.
func getPodName(podName string, anon bool, anonymizer *anonymizer) string {
	// If we aren't anonymizing the logs, just return the podName
	if !anon {
		return podName
	}

	// If this pod name has already been mapped, return that
	if anonName, ok := anonymizer.podNames[podName]; ok {
		return anonName
	}

	// the podName hasn't been anonymized yet so we split it up
	// so we can anonymize the namespace
	nameComponents := strings.SplitN(podName, ".", 2)
	if len(nameComponents) != 2 {
		unknownPodName := "anonPod.anonNamespace"
		anonymizer.podNames[podName] = unknownPodName
		return unknownPodName
	}
	var anonPodName, anonNamespace string
	name, namespace := nameComponents[0], nameComponents[1]
	if val, ok := anonymizer.namespaces[namespace]; ok {
		anonNamespace = val
	} else {
		anonNamespace = fmt.Sprintf("namespace-%d", len(anonymizer.namespaces)+1)
		anonymizer.namespaces[namespace] = anonNamespace
	}

	// we want to special case the traffic-manager so we can easily distinguish
	// between that and the traffic-agents
	if strings.Contains(name, "traffic-manager") {
		anonPodName = fmt.Sprintf("traffic-manager.%s", anonNamespace)
	} else {
		anonPodName = fmt.Sprintf("pod-%d.%s", len(anonymizer.podNames)+1, anonNamespace)
	}
	// Store the anonPodName in the map
	anonymizer.podNames[podName] = anonPodName
	return anonPodName
}

// anonymizeLog is a helper function that replaces the namespace + podName
// used in the log with its anonymized version, provided by the anonymizer.
func anonymizeLog(stdout io.Writer, logFile string, anonymizer *anonymizer) error {
	// Read the contents we are going to overwrite from the file
	content, err := os.ReadFile(logFile)
	if err != nil {
		return err
	}
	// Open the file with write so we can overwrite it
	stringContent := string(content)
	f, err := os.OpenFile(logFile, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	// First we replace the actual namespace with the anonymized
	// version.
	for namespace, anonNamespace := range anonymizer.namespaces {
		stringContent = strings.ReplaceAll(stringContent, namespace, anonNamespace)
	}
	// Now we do pod name which is a little bit more complicated
	for fullPodName, fullAnonPodName := range anonymizer.podNames {
		// strip the namespace off of the anonymized name
		anonPodParts := strings.Split(fullAnonPodName, ".")
		anonPodName := anonPodParts[0]

		// Strip the namespace off of the podName + replace pod name + full
		// hash with the anonymized name
		podParts := strings.Split(fullPodName, ".")
		podName := podParts[0]
		stringContent = strings.ReplaceAll(stringContent, podName, anonPodName)

		// Strip the hash off of the pod name + replace that with anonymized name
		brokenPodName := strings.Split(podName, "-")
		podName = strings.Join(brokenPodName[:len(brokenPodName)-2], "-")
		stringContent = strings.ReplaceAll(stringContent, podName, anonPodName)
	}

	// Overwrite the file with the new name
	err = f.Truncate(0)
	if err != nil {
		return err
	}
	_, err = f.Seek(0, 0)
	if err != nil {
		return err
	}
	fdWriter := bufio.NewWriter(f)
	_, err = fdWriter.WriteString(stringContent)
	if err != nil {
		return err
	}
	fdWriter.Flush()

	return nil
}
