package composectl

import (
	"fmt"
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/spf13/cobra"
)

type (
	manifestOptions struct {
		SrcStorePath *string
		Locally      *bool
	}
)

func init() {
	manifestCmd := &cobra.Command{
		Use:   "manifest <ref>",
		Short: "manifest <ref>",
		Long:  ``,
		Args:  cobra.ExactArgs(1),
	}
	opts := manifestOptions{}

	opts.SrcStorePath = manifestCmd.Flags().StringP("source-store-path", "l", "",
		"A path to the source store root directory")
	opts.Locally = manifestCmd.Flags().BoolP("local", "", false,
		"Print manifest of app stored locally")
	manifestCmd.Run = func(cmd *cobra.Command, args []string) {
		doOutputManifest(cmd, args, &opts)
	}

	showCmd.AddCommand(manifestCmd)
}

func doOutputManifest(cmd *cobra.Command, args []string, opts *manifestOptions) {
	if *opts.Locally && len(*opts.SrcStorePath) == 0 {
		opts.SrcStorePath = &config.StoreRoot
	}
	var blobProvider compose.BlobProvider
	if len(*opts.SrcStorePath) > 0 {
		blobProvider = compose.NewStoreBlobProvider(compose.GetBlobsRootFor(*opts.SrcStorePath))
	} else {
		blobProvider = compose.NewRemoteBlobProviderFromConfig(config)
	}
	b, err := compose.ReadBlobWithReadLimit(cmd.Context(), blobProvider, args[0], v1.AppManifestMaxSize)
	DieNotNil(err)
	fmt.Printf("%s\n", string(b))
}
