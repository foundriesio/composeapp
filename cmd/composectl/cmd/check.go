package composectl

import (
	"context"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/opencontainers/go-digest"
	"github.com/spf13/cobra"
)

var (
	checkCmd = &cobra.Command{
		Use:   "check",
		Short: "check <ref> [<ref>]",
		Long:  ``,
		Args:  cobra.MinimumNArgs(1),
		Run:   checkAppsCmd,
	}
	checkUsageWatermark *uint
)

type (
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
	rootCmd.AddCommand(checkCmd)
	checkUsageWatermark = checkCmd.Flags().UintP("storage-usage-watermark", "u", 80, "The maximum allowed storage usage in percentage")
}

func checkAppsCmd(cmd *cobra.Command, args []string) {
	cr, ui, _ := checkApps(cmd.Context(), args, *checkUsageWatermark)

	fmt.Printf("required: %d (%g%%), available: %d (%g%%) at %s, size: %d (100%%), free: %d (%g%%), reserved: %d (%g%%)\n",
		ui.Required, ui.RequiredP, ui.Available, ui.AvailableP, ui.Path, ui.SizeB, ui.Free, ui.FreeP, ui.Reserved, ui.ReservedP)

	fmt.Printf("%d blobs to pull; total download size: %d, total store size: %d, total runtime size of missing blobs: %d, total required %d...\n",
		len(cr.missingBlobs), cr.totalPullSize, cr.totalStoreSize, cr.totalRuntimeSize, cr.totalStoreSize+cr.totalRuntimeSize)
}

func checkApps(ctx context.Context, appRefs []string, usageWatermark uint) (*checkAppResult, *compose.UsageInfo, []compose.App) {
	if usageWatermark < MinUsageWatermark {
		DieNotNil(fmt.Errorf("the specified usage watermark is lower than the minimum allowed; %d < %d", usageWatermark, MinUsageWatermark))
	}
	if usageWatermark > MaxUsageWatermark {
		DieNotNil(fmt.Errorf("the specified usage watermark is higher than the maximum allowed; %d < %d", usageWatermark, MaxUsageWatermark))
	}

	cs, err := v1.NewAppStore(config.StoreRoot, config.Platform)
	DieNotNil(err)

	authorizer := compose.NewRegistryAuthorizer(config.DockerCfg)
	resolver := compose.NewResolver(authorizer)

	var apps []compose.App
	blobsToPull := map[digest.Digest]compose.BlobInfo{}
	checkRes := checkAppResult{missingBlobs: blobsToPull}

	for _, appRef := range appRefs {
		fmt.Printf("Loading %s metadata from registry...\n", appRef)
		app, tree, err := v1.NewAppLoader().LoadAppTree(ctx, compose.NewRemoteBlobProvider(resolver),
			platforms.OnlyStrict(config.Platform), appRef)
		DieNotNil(err)
		apps = append(apps, app)
		fmt.Printf("%s metadata loaded\n", app.Name())
		fmt.Printf("Checking %s state in the local store...\n", app.Name())

		var blockSize int64 = 4096
		s, err := compose.GetFsStat(config.StoreRoot)
		if err != nil {
			fmt.Printf("Failed to get FS block size: %s\n", err.Error())
			fmt.Printf("Assuming the FS block size if 4096")
		} else {
			blockSize = s.BlockSize
		}

		err = tree.Walk(func(node *compose.TreeNode, depth int) error {
			blobDescStr := fmt.Sprintf("%*s %10s %s", depth*8, " ", node.Type, node.Descriptor.Digest.Encoded())
			fmt.Printf("%s %*d", blobDescStr, 120-len(blobDescStr), node.Descriptor.Size)
			bs, stateCheckErr := compose.CheckBlob(ctx, cs, compose.WithExpectedDigest(node.Descriptor.Digest),
				compose.WithExpectedSize(node.Descriptor.Size))
			if stateCheckErr != nil {
				return stateCheckErr
			}
			fmt.Printf("...%s\n", bs.String())
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

		fmt.Println()
		if len(blobsToPull) == 0 {
			fmt.Printf("%s is in sync (%s)\n", app.Name(), appRef)
			continue
		}

		if !app.HasLayersMeta(config.Platform.Architecture) {
			fmt.Println("No app layers meta are found, the app layer sizes are approximated!")
		}

		for _, b := range blobsToPull {
			checkRes.totalPullSize += b.Descriptor.Size
			checkRes.totalStoreSize += b.StoreSize
			checkRes.totalRuntimeSize += b.RuntimeSize
		}
	}
	// TODO:  take into account that docker data root and OCI/blob store can be located on different volumes
	ui, err := compose.GetUsageInfo(config.StoreRoot, checkRes.totalStoreSize+checkRes.totalRuntimeSize, usageWatermark)
	if err != nil {
		fmt.Printf("Failed to get storage usage information")
	}
	return &checkRes, ui, apps
}
