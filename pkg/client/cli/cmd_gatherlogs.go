package cli

import (
	"archive/zip"
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

type gatherLogsArgs struct {
	outputFile     string
	daemons        string
	trafficAgents  string
	trafficManager bool
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
telepresence gather-logs --traffic-agents=False --traffic-manager=False

# Get all logs for pods that have "echo-easy" in the name, useful if you have multiple replicas
telepresence gather-logs --traffic-manager=False --traffic-agents=echo-easy

# Get all logs for a specific pod
telepresence gather-logs --traffic-manager=False --traffic-agents=echo-easy-6848967857-tw4jw     

# Get logs from everything except the daemons
telepresence gather-logs --daemons=False
`,

		RunE: func(cmd *cobra.Command, _ []string) error {
			return gl.gatherLogs(cmd, cmd.Context(), cmd.OutOrStdout())
		},
	}
	flags := cmd.Flags()
	flags.StringVarP(&gl.outputFile, "output-file", "o", "", "The file you want to output the logs to.")
	flags.StringVar(&gl.daemons, "daemons", "all", "The daemons you want logs from: all, root, user, False")
	flags.BoolVar(&gl.trafficManager, "traffic-manager", true, "If you want to collect logs from the traffic-manager")
	flags.StringVar(&gl.trafficAgents, "traffic-agents", "all", "Traffic-agents to collect logs from: all, name substring, False")
	return cmd
}

// gatherLogs gets the logs from the daemons (daemon + connector) and creates a zip
// file with their contents.
func (gl *gatherLogsArgs) gatherLogs(cmd *cobra.Command, ctx context.Context, stdout io.Writer) error {
	// Get the log directory and return the error if we can't get it
	logDir, err := filelocation.AppUserLogDir(ctx)
	if err != nil {
		return err
	}

	// If the user did not provide an outputFile, we'll use their current working directory
	if gl.outputFile == "" {
		pwd, err := os.Getwd()
		if err != nil {
			return err
		}
		gl.outputFile = fmt.Sprintf("%s/telepresence_logs.zip", pwd)
	} else if !strings.HasSuffix(gl.outputFile, ".zip") {
		return errors.New("output file must end in .zip")
	}

	// Create a temporary directory where we will store the logs before we zip
	// them for export
	exportDir, err := os.MkdirTemp("", "logexp-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(exportDir); err != nil {
			fmt.Fprintf(stdout, "Failed to remove temp directory %s: %s", exportDir, err)
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
	case "False":
	default:
		return errors.New("Options for --daemons are: all, root, user, or False")
	}

	// Get all logs from the logdir that match the daemons the user cares about.
	logFiles, err := os.ReadDir(logDir)
	if err != nil {
		return err
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
					fmt.Fprintf(stdout, "failed exporting %s: %s\n", entry.Name(), err)
					continue
				}
			}
		}
	}

	// Since getting the logs from k8s requires the connector, let's only do this
	// work if we know the user wants to get logs from k8s.
	if gl.trafficManager || gl.trafficAgents != "False" {
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
			fmt.Fprintf(stdout, "Error getting logs from k8s components: %s", err)
		}
	}

	// Zip up all the files we've created in the zip directory and return that to the user
	var files []string
	dirEntries, err := os.ReadDir(exportDir)
	if err != nil {
		return err
	}
	for _, entry := range dirEntries {
		if !entry.IsDir() {
			files = append(files, fmt.Sprintf("%s/%s", exportDir, entry.Name()))
		}
	}

	if err := zipFiles(files, gl.outputFile); err != nil {
		return err
	}

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
		info, err := fd.Stat()
		if err != nil {
			return err
		}
		baseName := info.Name()
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
