package composectl

import (
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/spf13/cobra"
)

var (
	composeCmd = &cobra.Command{
		Use:   "compose <ref>",
		Short: "compose <ref>",
		Long:  ``,
		Args:  cobra.ExactArgs(1),
	}
)

type (
	composeOptions struct {
		SrcStorePath *string
		Locally      *bool
	}
)

func init() {
	opts := composeOptions{}

	opts.SrcStorePath = composeCmd.Flags().StringP("source-store-path", "l", "",
		"A path to the source store root directory")
	opts.Locally = composeCmd.Flags().BoolP("local", "", false,
		"Print compose config/file of app stored locally")
	composeCmd.Run = func(cmd *cobra.Command, args []string) {
		doOutputComposeFile(cmd, args, &opts)
	}

	showCmd.AddCommand(composeCmd)
}

func doOutputComposeFile(cmd *cobra.Command, args []string, opts *composeOptions) {
	if *opts.Locally && len(*opts.SrcStorePath) == 0 {
		opts.SrcStorePath = &config.StoreRoot
	}
	var blobProvider compose.BlobProvider
	if len(*opts.SrcStorePath) > 0 {
		blobProvider = compose.NewStoreBlobProvider(compose.GetBlobsRootFor(*opts.SrcStorePath))
	} else {
		authorizer := compose.NewRegistryAuthorizer(config.DockerCfg, config.ConnectTimeout)
		resolver := compose.NewResolver(authorizer, config.ConnectTimeout)
		blobProvider = compose.NewRemoteBlobProvider(resolver)
	}
	app, err := v1.NewAppLoader().LoadAppTree(cmd.Context(), blobProvider, platforms.OnlyStrict(config.Platform), args[0])
	DieNotNil(err)
	composeProject, err := app.GetCompose(cmd.Context(), blobProvider)
	DieNotNil(err)
	b, err := composeProject.MarshalYAML()
	DieNotNil(err)
	fmt.Printf("%s", string(b))
}
