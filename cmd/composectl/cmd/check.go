package composectl

import (
	"context"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/reference"
	dockertypes "github.com/docker/docker/api/types"
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
		Run:   checkAppsCmd,
	}
	checkUsageWatermark  *uint
	checkSrcStorePath    *string
	setExitCodeIfMissing *bool
	quiet                *bool
	checkInstalled       *bool
	checkLocally         *bool
)

type (
	checkAppResult struct {
		missingBlobs     map[digest.Digest]compose.BlobInfo
		totalPullSize    int64
		totalStoreSize   int64
		totalRuntimeSize int64
	}
	installCheckResult map[string][]string
)

const (
	MinUsageWatermark = 20
	MaxUsageWatermark = 95
)

func init() {
	rootCmd.AddCommand(checkCmd)
	checkUsageWatermark = checkCmd.Flags().UintP("storage-usage-watermark", "u", 80, "The maximum allowed storage usage in percentage")
	checkSrcStorePath = checkCmd.Flags().StringP("source-store-path", "l", "", "A path to the source store root directory")
	setExitCodeIfMissing = checkCmd.Flags().BoolP("set-exit-code", "e", false, "Set an exit code to 1 if at least one of app blobs is missing")
	quiet = checkCmd.Flags().BoolP("quiet", "q", false, "Do not print app details")
	checkInstalled = checkCmd.Flags().BoolP("check-installed", "d", false, "Check if app is installed (loaded into the docker store)")
	checkLocally = checkCmd.Flags().BoolP("local", "", false, "Check if app is fetched locally without getting app manifest from registry")
}

func checkAppsCmd(cmd *cobra.Command, args []string) {
	if *checkLocally && len(*checkSrcStorePath) == 0 {
		checkSrcStorePath = &config.StoreRoot
	}
	cr, ui, _ := checkApps(cmd.Context(), args, *checkUsageWatermark, *checkSrcStorePath, *quiet)
	if len(cr.missingBlobs) > 0 {
		ui.Print()
		cr.print()
	}
	var ic *installCheckResult
	var err error
	if *checkInstalled {
		ic, err = checkIfInstalled(cmd.Context(), args, config.StoreRoot, config.DockerHost, *quiet)
		DieNotNil(err)
	}
	if *setExitCodeIfMissing && (len(cr.missingBlobs) > 0 || (ic != nil && len(*ic) > 0)) {
		os.Exit(1)
	}
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
		if err != nil {
			fmt.Printf("Failed to get FS block size: %s\n", err.Error())
			fmt.Printf("Assuming the FS block size if 4096")
		} else {
			blockSize = s.BlockSize
		}

		err = tree.Walk(func(node *compose.TreeNode, depth int) error {
			blobDescStr := fmt.Sprintf("%*s %10s %s", depth*8, " ", node.Type, node.Descriptor.Digest.Encoded())
			if !quiet {
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
			}
		}
		if len(blobsToPull) == 0 {
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

func (cr *checkAppResult) print() {
	fmt.Printf("%d blobs to pull; total download size: %s, total store size: %s, total runtime size of missing blobs: %s, total required: %s\n",
		len(cr.missingBlobs), units.BytesSize(float64(cr.totalPullSize)), units.BytesSize(float64(cr.totalStoreSize)), units.BytesSize(float64(cr.totalRuntimeSize)), units.BytesSize(float64(cr.totalStoreSize+cr.totalRuntimeSize)))
}

func checkIfInstalled(ctx context.Context, appRefs []string, srcStorePath string, dockerHost string, quiet bool) (*installCheckResult, error) {
	cli, err := compose.GetDockerClient(dockerHost)
	if err != nil {
		return nil, err
	}
	images, err := cli.ImageList(ctx, dockertypes.ImageListOptions{All: true})
	if err != nil {
		return nil, err
	}
	installedImages := map[string]bool{}
	for _, i := range images {
		if len(i.RepoDigests) > 0 {
			installedImages[i.RepoDigests[0]] = true
		}
		if len(i.RepoTags) > 0 {
			// unpatch docker won't store the digest URI of loaded image
			installedImages[i.RepoTags[0]] = true
		}
	}

	checkResult := installCheckResult{}
	blobProvider := compose.NewStoreBlobProvider(path.Join(srcStorePath, "blobs", "sha256"))
	for _, appRef := range appRefs {
		app, _, err := v1.NewAppLoader().LoadAppTree(ctx, blobProvider, platforms.OnlyStrict(config.Platform), appRef)
		DieNotNil(err)
		var missingImages []string
		appComposeRoot := app.GetComposeRoot()
		for _, imageNode := range appComposeRoot.Children {
			imageUri := imageNode.Descriptor.URLs[0]
			if !installedImages[imageUri] {
				if s, err := reference.Parse(imageUri); err == nil {
					taggedUri := s.Locator + ":" + (s.Digest().Encoded())[:7]
					if !installedImages[taggedUri] {
						missingImages = append(missingImages, imageUri)
					}
				}
			}
		}
		if len(missingImages) > 0 {
			checkResult[appRef] = missingImages
			fmt.Printf("%s is not installed (%s); missing images:\n", app.Name(), appRef)
			for _, image := range missingImages {
				fmt.Printf("\t- %s\n", image)
			}
		} else if !quiet {
			fmt.Printf("%s is installed (%s)\n", app.Name(), appRef)
		}
	}
	return &checkResult, nil
}
