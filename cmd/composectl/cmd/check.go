package composectl

import (
	"context"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/docker/go-units"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/opencontainers/go-digest"
	"github.com/spf13/cobra"
	"os"
	"path"
)

var (
	checkCmd = &cobra.Command{
		Use:   "check",
		Short: "check <ref> [<ref>]",
		Long:  ``,
		Args:  cobra.MinimumNArgs(1),
	}
)

type (
	checkOptions struct {
		UsageWatermark *uint
		SrcStorePath   *string
		Locally        *bool
		Format         string
	}

	checkAppResult struct {
		missingBlobs     map[digest.Digest]compose.BlobInfo
		totalPullSize    int64
		totalStoreSize   int64
		totalRuntimeSize int64
	}
)

const (
	MinUsageWatermark = 20
	MaxUsageWatermark = 95
)

func init() {
	opts := checkOptions{}
	opts.UsageWatermark = checkCmd.Flags().UintP("storage-usage-watermark", "u", 80,
		"The maximum allowed storage usage in percentage")
	opts.SrcStorePath = checkCmd.Flags().StringP("source-store-path", "l", "",
		"A path to the source store root directory")
	opts.Locally = checkCmd.Flags().BoolP("local", "", false,
		"Check whether app is fetched without getting app manifest from registry")
	checkCmd.Flags().StringVar(&opts.Format, "format", "plain",
		"Output format; supported: plain, json")
	checkCmd.Run = func(cmd *cobra.Command, args []string) {
		if opts.Format != "plain" && opts.Format != "json" {
			DieNotNil(cmd.Usage())
			fmt.Fprintf(os.Stderr, "unsupported  `--format` value: %s\n", opts.Format)
			os.Exit(1)
		}
		checkAppsCmd(cmd, args, &opts)
	}

	rootCmd.AddCommand(checkCmd)
}

func checkAppsCmd(cmd *cobra.Command, args []string, opts *checkOptions) {
	if *opts.Locally && len(*opts.SrcStorePath) == 0 {
		// Use the local store as the source store to check whether app is fetched without a need in connection to Registry.
		// Requires app manifest and app archive presence in the local store, otherwise fails.
		opts.SrcStorePath = &config.StoreRoot
	}
	cr, ui, _ := checkApps(cmd.Context(), args, *opts.UsageWatermark, *opts.SrcStorePath, false)
	ui.Print()
	cr.print()
}

func checkApps(ctx context.Context, appRefs []string, usageWatermark uint, srcStorePath string, quiet bool) (*checkAppResult, *compose.UsageInfo, []compose.App) {
	if usageWatermark < MinUsageWatermark {
		DieNotNil(fmt.Errorf("the specified usage watermark is lower than the minimum allowed; %d < %d", usageWatermark, MinUsageWatermark))
	}
	if usageWatermark > MaxUsageWatermark {
		DieNotNil(fmt.Errorf("the specified usage watermark is higher than the maximum allowed; %d < %d", usageWatermark, MaxUsageWatermark))
	}

	cs, err := v1.NewAppStore(config.StoreRoot, config.Platform)
	DieNotNil(err)

	var localSrcStore string
	var blobProvider compose.BlobProvider
	if len(srcStorePath) > 0 {
		blobProvider = compose.NewStoreBlobProvider(path.Join(srcStorePath, "blobs", "sha256"))
	} else {
		authorizer := compose.NewRegistryAuthorizer(config.DockerCfg)
		resolver := compose.NewResolver(authorizer)
		blobProvider = compose.NewRemoteBlobProvider(resolver)
	}

	var apps []compose.App
	blobsToPull := map[digest.Digest]compose.BlobInfo{}
	checkRes := checkAppResult{missingBlobs: blobsToPull}

	for _, appRef := range appRefs {
		if !quiet {
			if len(localSrcStore) > 0 {
				fmt.Printf("Loading %s metadata from %s...\n", appRef, localSrcStore)
			} else {
				fmt.Printf("Loading %s metadata from registry...\n", appRef)
			}
		}
		app, tree, err := v1.NewAppLoader().LoadAppTree(ctx, blobProvider, platforms.OnlyStrict(config.Platform), appRef)
		DieNotNil(err)
		apps = append(apps, app)
		if !quiet {
			fmt.Printf("%s metadata loaded\n", app.Name())
			fmt.Printf("Checking %s state in the local store...\n", app.Name())
		}
		var blockSize int64 = 4096
		s, err := compose.GetFsStat(config.StoreRoot)
		if err != nil && !quiet {
			fmt.Printf("Failed to get FS block size: %s\n", err.Error())
			fmt.Printf("Assuming the FS block size if 4096")
		} else {
			blockSize = s.BlockSize
		}

		err = tree.Walk(func(node *compose.TreeNode, depth int) error {
			if !quiet {
				blobDescStr := fmt.Sprintf("%*s %10s %s", depth*8, " ", node.Type, node.Descriptor.Digest.Encoded())
				fmt.Printf("%s %*d", blobDescStr, 120-len(blobDescStr), node.Descriptor.Size)
			}
			bs, stateCheckErr := compose.CheckBlob(ctx, cs, compose.WithExpectedDigest(node.Descriptor.Digest),
				compose.WithExpectedSize(node.Descriptor.Size))
			if stateCheckErr != nil {
				return stateCheckErr
			}
			if !quiet {
				fmt.Printf("...%s\n", bs.String())
			}
			if bs != compose.BlobOk {
				blobsToPull[node.Descriptor.Digest] = compose.BlobInfo{
					Descriptor:  node.Descriptor,
					State:       bs,
					Type:        node.Type,
					StoreSize:   compose.AlignToBlockSize(node.Descriptor.Size, blockSize),
					RuntimeSize: app.GetBlobRuntimeSize(node.Descriptor, config.Platform.Architecture, blockSize),
				}
			}
			return nil
		})
		DieNotNil(err)

		if !quiet {
			fmt.Println()
			if len(blobsToPull) == 0 {
				fmt.Printf("%s is in sync (%s)\n", app.Name(), appRef)
				continue
			}

			if !app.HasLayersMeta(config.Platform.Architecture) {
				fmt.Println("No app layers meta are found, the app layer sizes are approximated!")
			}
		}

		for _, b := range blobsToPull {
			checkRes.totalPullSize += b.Descriptor.Size
			checkRes.totalStoreSize += b.StoreSize
			checkRes.totalRuntimeSize += b.RuntimeSize
		}
	}
	// TODO:  take into account that docker data root and OCI/blob store can be located on different volumes
	ui, err := compose.GetUsageInfo(config.StoreRoot, checkRes.totalStoreSize+checkRes.totalRuntimeSize, usageWatermark)
	if err != nil && !quiet {
		fmt.Printf("Failed to get storage usage information")
	}
	return &checkRes, ui, apps
}

func (cr *checkAppResult) print() {
	fmt.Printf("%d blobs to pull; total download size: %s, total store size: %s, total runtime size of missing blobs: %s, total required: %s\n",
		len(cr.missingBlobs), units.BytesSize(float64(cr.totalPullSize)), units.BytesSize(float64(cr.totalStoreSize)), units.BytesSize(float64(cr.totalRuntimeSize)), units.BytesSize(float64(cr.totalStoreSize+cr.totalRuntimeSize)))
}
