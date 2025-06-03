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
			sig := <-sigChan
			fmt.Printf(" Received signal: %v, stopping...", sig)
			cancel()
			fmt.Println("ok")
		}()

		cmd.SetContext(ctx)
	},
}
