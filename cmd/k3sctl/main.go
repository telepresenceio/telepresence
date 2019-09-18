package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/datawire/teleproxy/pkg/dtest"
)

// Version holds the version of the code. This is intended to be overridden at build time.
var Version = "(unknown version)"

func main() {
	var k3s = &cobra.Command{
		Use:           "k3sctl",
		Short:         "k3sctl",
		Long:          "k3sctl - manage a local docker registry and kubernetes cluster",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	var logFile *os.File

	k3s.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		logFile, err := os.OpenFile("/tmp/k3sctl.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
		if err != nil {
			return err
		}

		log.SetOutput(logFile)
		return nil
	}

	var up = &cobra.Command{
		Use:   "up",
		Short: "ensure the k3s and registry containers are up",
	}

	k3s.AddCommand(up)

	up.RunE = func(cmd *cobra.Command, args []string) error {
		regid := dtest.RegistryUp()
		k3sid := dtest.K3sUp()
		fmt.Printf("DOCKER_CONTAINER=%q\n", regid)
		fmt.Printf("K3S_CONTAINER=%q\n", k3sid)
		return nil
	}

	var down = &cobra.Command{
		Use:   "down",
		Short: "shutdown the k3s and registry containers",
	}

	k3s.AddCommand(down)

	down.RunE = func(cmd *cobra.Command, args []string) error {
		k3sid := dtest.K3sDown()
		regid := dtest.RegistryDown()
		fmt.Printf("Shutdown k3s container: %s\n", k3sid)
		fmt.Printf("Shutdown registry container: %s\n", regid)
		return nil
	}

	var registry = &cobra.Command{
		Use:   "registry",
		Short: "print the docker registry url",
	}

	k3s.AddCommand(registry)

	registry.RunE = func(cmd *cobra.Command, args []string) error {
		fmt.Println(dtest.DockerRegistry())
		return nil
	}

	var config = &cobra.Command{
		Use:   "config",
		Short: "print the k3s config",
	}

	output := config.Flags().StringP("output", "o", "", "path for kubeconfig file")

	k3s.AddCommand(config)

	config.RunE = func(cmd *cobra.Command, args []string) error {
		kubeconfig := dtest.GetKubeconfig()
		if kubeconfig == "" {
			return errors.New("no k3s cluster is running")
		}

		if *output == "" {
			fmt.Print(kubeconfig)
			return nil
		}

		err := ioutil.WriteFile(*output, []byte(kubeconfig), 0644)
		if err == io.EOF {
			err = nil
		}
		if err != nil {
			return err
		}

		fmt.Printf("Wrote kubeconfig to %s.\n", *output)

		return nil
	}

	var version = &cobra.Command{
		Use:   "version",
		Short: "print the k3sctl version",
	}

	k3s.AddCommand(version)

	version.RunE = func(cmd *cobra.Command, args []string) error {
		fmt.Println("k3sctl", "version", Version)
		return nil
	}

	err := k3s.Execute()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		logFile.Close()
		os.Exit(1)
	}
}
