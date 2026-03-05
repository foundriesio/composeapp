package composectl

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var (
	Version string
	Commit  string
	Date    string
)

type (
	VersionInfo struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		Date    string `json:"build_date"`
	}

	versionOptions struct {
		Format string
		Short  bool
	}
)

func init() {
	opts := versionOptions{}

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the version information",
		Args:  cobra.NoArgs,
	}

	cmd.Flags().StringVar(&opts.Format, "format", "text", "Format the output. Values: [text | json]")
	cmd.Flags().BoolVar(&opts.Short, "short", false, "Print only the version number")

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		info := VersionInfo{
			Version: Version,
			Commit:  Commit,
			Date:    Date,
		}
		switch opts.Format {
		case "json":
			var data []byte
			var err error
			if opts.Short {
				data, err = json.Marshal(info.Version)
			} else {
				data, err = json.MarshalIndent(info, "", "  ")
			}
			DieNotNil(err, "failed to marshal version info")
			fmt.Println(string(data))
		case "text":
			if opts.Short {
				fmt.Println(info.Version)
			} else {
				fmt.Printf("Version: %s\nCommit: %s\nBuild Date: %s\n", info.Version, info.Commit, info.Date)
			}
		default:
			return fmt.Errorf("invalid value for --format: %s (must be text or json)", opts.Format)
		}
		return nil
	}

	rootCmd.AddCommand(cmd)
}
