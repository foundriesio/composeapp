//go:build publish

package composectl

import (
	"errors"
	"fmt"
	"github.com/docker/distribution/reference"
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/opencontainers/go-digest"
	"github.com/spf13/cobra"
	"log"
	"strings"
)

const (
	banner = `
	   |\/|
	\__|__|__/
	`
)

var publishCmd = &cobra.Command{
	Use:   "publish <ref> [comma,separated,arch,list]",
	Short: "publish <ref> [comma,separated,arch,list]",
	Args:  cobra.RangeArgs(1, 2),
}

type (
	publishOptions struct {
		ComposeFile             string
		DigestFile              string
		DryRun                  bool
		PinnedImageURIs         []string
		LayersMetaFile          string
		CreateAppLayersManifest bool
	}
)

func init() {
	opts := publishOptions{}
	publishCmd.Flags().StringVarP(&opts.ComposeFile, "file", "f", "docker-compose.yml", "A path to a compose project file")
	publishCmd.Flags().StringVarP(&opts.DigestFile, "digest-file", "d", "", "A file to store the published app sha256 digest to")
	publishCmd.Flags().BoolVar(&opts.DryRun, "dryrun", false, "Show what would be done, but don't actually publish")
	publishCmd.Flags().StringArrayVar(&opts.PinnedImageURIs, "pinned-images", nil, "A list of app images referred through digest URIs to pin app to")
	publishCmd.Flags().StringVarP(&opts.LayersMetaFile, "layers-meta", "l", "", "Json file containing App layers' metadata (size, usage)")
	publishCmd.Flags().BoolVar(&opts.CreateAppLayersManifest, "layers-manifest", true, "Add app layers manifests to the app manifest")

	publishCmd.Run = func(cmd *cobra.Command, args []string) {
		fmt.Println(banner)
		appRef, err := compose.ParseAppRef(args[0])
		if err != nil {
			DieNotNil(err, "invalid app reference specified")
		}
		if len(appRef.Digest) > 0 {
			DieNotNil(fmt.Errorf("invalid app reference specified: cannot be a reference with digest"))
		}
		if len(appRef.Tag) == 0 {
			DieNotNil(fmt.Errorf("invalid app reference specified: must be reference with a tag"))
		}
		var archList []string
		if len(args) > 1 {
			archList = strings.Split(args[1], ",")
		}
		publishApp(cmd, appRef, archList, &opts)
	}
	rootCmd.AddCommand(publishCmd)
}

func publishApp(cmd *cobra.Command, appRef *compose.AppRef, archList []string, opts *publishOptions) {
	if len(archList) == 0 {
		log.Println("Architecture list is not specified," +
			" intersection of all App's images architectures will be supported by App")
	}

	pinnedImages := map[string]digest.Digest{}
	for _, uri := range opts.PinnedImageURIs {
		named, err := reference.ParseNormalizedNamed(uri)
		if err != nil {
			DieNotNil(err, "Invalid image URI specified in `pinned-images`: "+err.Error())
		}
		if digested, ok := named.(reference.Digested); ok {
			pinnedImages[named.Name()] = digested.Digest()
		} else {
			DieNotNil(errors.New("Image URI specified in `pinned-images` is not digested: " + uri))
		}
	}

	DieNotNil(compose.DoPublish(cmd.Context(), appRef.Name, opts.ComposeFile, appRef.String(), opts.DigestFile,
		opts.DryRun, archList, pinnedImages, opts.LayersMetaFile, opts.CreateAppLayersManifest))
}
