package composectl

import (
	"encoding/json"
	"fmt"
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "ls",
	Short: "list apps found in the store",
	Long:  ``,
	Args:  cobra.NoArgs,
}

type (
	listOptions struct {
		Format string
	}
	AppJsonOutput struct {
		Name string `json:"name"`
		URI  string `json:"uri"`
	}
)

func init() {
	opts := listOptions{}
	listCmd.Flags().StringVar(&opts.Format, "format", "plain", "Format the output. Values: [plain | json]")
	listCmd.Run = func(cmd *cobra.Command, args []string) {
		if opts.Format != "plain" && opts.Format != "json" {
			DieNotNil(fmt.Errorf("invalid value of `--format` option: %s", opts.Format))
		}
		listApps(cmd, &opts)
	}
	rootCmd.AddCommand(listCmd)
}

func listApps(cmd *cobra.Command, opts *listOptions) {
	apps, err := compose.ListApps(cmd.Context(), config)
	DieNotNil(err)
	if opts.Format == "json" {
		var lsOutput []AppJsonOutput
		for _, app := range apps {
			lsOutput = append(lsOutput, AppJsonOutput{
				Name: app.Name(),
				URI:  app.Ref().String(),
			})
		}
		if b, err := json.MarshalIndent(lsOutput, "", "  "); err == nil {
			fmt.Println(string(b))
		}
	} else {
		for _, a := range apps {
			fmt.Printf("%s -> %s\n", a.Name(), a.Ref())
		}
	}
}
