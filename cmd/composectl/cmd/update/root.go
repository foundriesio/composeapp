package updatectl

import (
	"context"
	"fmt"
	"github.com/spf13/cobra"
	"os"
	"os/signal"
)

var UpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "update apps",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		ctx, cancel := context.WithCancel(cmd.Context())
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt)
		go func() {
			<-sigChan
			// TODO: Add debug level log printing the signal details
			cancel()
			// TODO: Add debug level log informing that the command was cancelled
			fmt.Println()
		}()
		cmd.SetContext(ctx)
	},
}
