package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/spf13/cobra"

	"github.com/datawire/teleproxy/pkg/k8s"
)

func main() {
	var st = &cobra.Command{
		Use:           "kubestatus <kind>",
		Short:         "get and set status of kubernetes resources",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	info := k8s.NewKubeInfoFromFlags(st.Flags())
	fields := st.Flags().StringP("field-selector", "f", "", "field selector")
	labels := st.Flags().StringP("label-selector", "l", "", "label selector")
	statusFile := st.Flags().StringP("update", "u", "", "update with new status from file (must be json)")

	st.RunE = func(cmd *cobra.Command, args []string) error {
		var status map[string]interface{}

		if *statusFile != "" {
			rawStatus, err := os.Open(*statusFile)
			if err != nil {
				return err
			}
			defer rawStatus.Close()

			dec := json.NewDecoder(rawStatus)
			err = dec.Decode(&status)
			if err != nil {
				return err
			}
		}

		kind := args[0]
		namespace, err := info.Namespace()
		if err != nil {
			return err
		}

		w := k8s.MustNewWatcher(info)
		err = w.WatchQuery(k8s.Query{
			Kind:          kind,
			Namespace:     namespace,
			FieldSelector: *fields,
			LabelSelector: *labels,
		}, func(w *k8s.Watcher) {
			for _, rsrc := range w.List(kind) {
				if *statusFile == "" {
					fmt.Println("Status of", rsrc.QName())
					fmt.Printf("  %v\n", rsrc["status"])
				} else {
					fmt.Println("Updating", rsrc.QName())
					rsrc["status"] = status
					_, err := w.UpdateStatus(rsrc)
					if err != nil {
						log.Printf("error updating resource: %v", err)
					}
				}
			}
			w.Stop()
		})

		if err != nil {
			return err
		}

		w.Wait()
		return nil
	}

	err := st.Execute()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

}
