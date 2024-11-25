package composectl

import (
	"github.com/spf13/cobra"
)

var showCmd = &cobra.Command{
	Use:   "show",
	Short: "output app manifest or compose file",
}

func init() {
	rootCmd.AddCommand(showCmd)
}
