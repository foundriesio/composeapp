package composectl

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/reference"
	"github.com/containerd/containerd/reference/docker"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/go-units"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/opencontainers/go-digest"
	"github.com/spf13/cobra"
	"os"
)

type (
	checkOptions struct {
		UsageWatermark *uint
		SrcStorePath   *string
		Locally        *bool
		Format         string
		CheckInstall   bool
		Quick          bool
	}

	CheckAppResult struct {
		MissingBlobs     map[digest.Digest]compose.BlobInfo `json:"missing_blobs"`
		TotalPullSize    int64                              `json:"total_pull_size"`
		TotalStoreSize   int64                              `json:"total_store_size"`
		TotalRuntimeSize int64                              `json:"total_runtime_size"`
	}

	CheckAndInstallResult struct {
		FetchCheck   *CheckAppResult     `json:"fetch_check"`
		InstallCheck *InstallCheckResult `json:"install_check"`
	}

	AppInstallCheckResult struct {
		AppName       string                `json:"app_name"`
		MissingImages []string              `json:"missing_images"`
		BundleErrors  compose.AppBundleErrs `json:"bundle_errors"`
	}

	InstallCheckResult map[string]*AppInstallCheckResult
)

const (
	MinUsageWatermark = 20
	MaxUsageWatermark = 95
)

func init() {
	checkCmd := &cobra.Command{
		Use:   "check",
		Short: "check <ref> [<ref>]",
		Long:  ``,
		Args:  cobra.MinimumNArgs(1),
	}
	opts := checkOptions{}
	opts.UsageWatermark = checkCmd.Flags().UintP("storage-usage-watermark", "u", 80,
		"The maximum allowed storage usage in percentage")
	opts.SrcStorePath = checkCmd.Flags().StringP("source-store-path", "l", "",
		"A path to the source store root directory")
	opts.Locally = checkCmd.Flags().BoolP("local", "", false,
		"Check whether app is fetched without getting app manifest from registry")
	checkCmd.Flags().StringVar(&opts.Format, "format", "plain",
		"Output format; supported: plain, json")
	checkCmd.Flags().BoolVar(&opts.CheckInstall, "install", false,
		"Check both whether app is fetched and installed")
	checkCmd.Flags().BoolVar(&opts.Quick, "quick", false,
		"Skip checking hash of app blobs; verify only their presence and size")
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
	quietCheck := false
	if opts.Format == "json" {
		quietCheck = true
	}

	blobProvider, cs, err := getAppStoreAndDstBlobProvider(*opts.SrcStorePath, *opts.Locally)
	DieNotNil(err)
	if len(*opts.SrcStorePath) == 0 && *opts.Locally {
		opts.SrcStorePath = &config.StoreRoot
	}
	cr, ui, _ := checkApps(cmd.Context(), args, cs, blobProvider,
		*opts.UsageWatermark, *opts.SrcStorePath, quietCheck, opts.Quick)
	var ir InstallCheckResult
	if opts.CheckInstall {
		ir, err = checkIfInstalled(cmd.Context(), args, cs, config.DockerHost)
		DieNotNil(err)
	}
	if opts.Format == "json" {
		aggregatedCheckRes :=
			struct {
				FetchCheck   *CheckAppResult     `json:"fetch_check"`
				InstallCheck *InstallCheckResult `json:"install_check"`
			}{
				FetchCheck:   cr,
				InstallCheck: &ir,
			}
		if b, err := json.MarshalIndent(aggregatedCheckRes, "", "  "); err == nil {
			fmt.Println(string(b))
		} else {
			DieNotNil(err)
		}
	} else {
		ui.Print()
		cr.print()
		if opts.CheckInstall {
			for appRef, r := range ir {
				if len(r.MissingImages) > 0 || len(r.BundleErrors) > 0 {
					fmt.Printf("%s is not installed (%s)\n", r.AppName, appRef)
					if len(r.MissingImages) > 0 {
						fmt.Println("\tmissing images:")
						for _, val := range r.MissingImages {
							fmt.Println("\t\t" + val)
						}
					}
					if len(r.BundleErrors) > 0 {
						fmt.Println("\tapp bundle errors:")
						for f, e := range r.BundleErrors {
							fmt.Printf("\t\t%s:\t%s\n", f, e)
						}
					}
				}
			}
		}
	}
}

func checkApps(ctx context.Context,
	appRefs []string,
	appStoreBlobProvider compose.BlobProvider,
	srcBlobProvider compose.BlobProvider,
	usageWatermark uint,
	srcStorePath string,
	quiet bool,
	quick bool) (*CheckAppResult, *compose.UsageInfo, []compose.App) {
	if usageWatermark < MinUsageWatermark {
		DieNotNil(fmt.Errorf("the specified usage watermark is lower than the minimum allowed; %d < %d", usageWatermark, MinUsageWatermark))
	}
	if usageWatermark > MaxUsageWatermark {
		DieNotNil(fmt.Errorf("the specified usage watermark is higher than the maximum allowed; %d < %d", usageWatermark, MaxUsageWatermark))
	}

	var apps []compose.App
	blobsToPull := map[digest.Digest]compose.BlobInfo{}
	checkRes := CheckAppResult{MissingBlobs: blobsToPull}

	for _, appRef := range appRefs {
		if !quiet {
			if len(srcStorePath) > 0 {
				fmt.Printf("Loading %s metadata from %s...\n", appRef, srcStorePath)
			} else {
				fmt.Printf("Loading %s metadata from registry...\n", appRef)
			}
		}
		app, err := v1.NewAppLoader().LoadAppTree(ctx, srcBlobProvider, platforms.OnlyStrict(config.Platform), appRef)
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

		err = app.Tree().Walk(func(node *compose.TreeNode, depth int) error {
			if !quiet {
				blobDescStr := fmt.Sprintf("%*s %10s %s", depth*8, " ", node.Type, node.Descriptor.Digest.Encoded())
				fmt.Printf("%s %*d", blobDescStr, 120-len(blobDescStr), node.Descriptor.Size)
			}
			checkOpts := []compose.SecureReadOptions{compose.WithExpectedSize(node.Descriptor.Size)}
			if !quick {
				checkOpts = append(checkOpts, compose.WithExpectedDigest(node.Descriptor.Digest))
			}
			if node.HasRef() {
				checkOpts = append(checkOpts, compose.WithRef(node.Ref()))
			}
			bs, stateCheckErr := compose.CheckBlob(compose.WithAppRef(compose.WithBlobType(ctx, node.Type), app.Ref()),
				appStoreBlobProvider, node.Descriptor.Digest, checkOpts...)
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
			checkRes.TotalPullSize += b.Descriptor.Size
			checkRes.TotalStoreSize += b.StoreSize
			checkRes.TotalRuntimeSize += b.RuntimeSize
		}
	}
	// TODO:  take into account that docker data root and OCI/blob store can be located on different volumes
	ui, err := compose.GetUsageInfo(config.StoreRoot, checkRes.TotalStoreSize+checkRes.TotalRuntimeSize, usageWatermark)
	if err != nil && !quiet {
		fmt.Printf("Failed to get storage usage information")
	}
	return &checkRes, ui, apps
}

func (cr *CheckAppResult) print() {
	fmt.Printf("%d blobs to pull; total download size: %s, total store size: %s, total runtime size of missing blobs: %s, total required: %s\n",
		len(cr.MissingBlobs), units.BytesSize(float64(cr.TotalPullSize)), units.BytesSize(float64(cr.TotalStoreSize)), units.BytesSize(float64(cr.TotalRuntimeSize)), units.BytesSize(float64(cr.TotalStoreSize+cr.TotalRuntimeSize)))
}

func checkIfInstalled(ctx context.Context, appRefs []string, blobProvider compose.BlobProvider, dockerHost string) (InstallCheckResult, error) {
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

	checkResult := InstallCheckResult{}
	for _, appRef := range appRefs {
		app, err := v1.NewAppLoader().LoadAppTree(ctx, blobProvider, platforms.OnlyStrict(config.Platform), appRef)
		DieNotNil(err)
		var missingImages []string
		appComposeRoot := app.GetComposeRoot()
		for _, imageNode := range appComposeRoot.Children {
			imageUri := imageNode.Ref()
			if !installedImages[imageUri] {
				if s, err := reference.Parse(imageUri); err == nil {
					taggedUri := s.Locator + ":" + (s.Digest().Encoded())[:7]
					if !installedImages[taggedUri] {
						// Check familiar name
						if anyRef, err := docker.ParseAnyReference(imageUri); err == nil {
							familiarRef := docker.FamiliarString(anyRef)
							if !installedImages[familiarRef] {
								missingImages = append(missingImages, imageUri)
							}
						}
					}
				}
			}
		}
		errMap, err := app.CheckComposeInstallation(ctx, blobProvider, config.GetAppComposeDir(app.Name()))
		if err != nil {
			return nil, err
		}
		checkResult[appRef] = &AppInstallCheckResult{
			AppName:       app.Name(),
			MissingImages: missingImages,
			BundleErrors:  errMap,
		}
	}
	return checkResult, nil
}

func getAppStoreAndDstBlobProvider(srcStorePath string, local bool) (srcBlobProvider compose.BlobProvider, store compose.AppStore, err error) {
	// Create the skopeo store aware instance only if it is a local check
	store, err = v1.NewAppStore(config.StoreRoot, config.Platform, local)
	if err != nil {
		return
	}
	if len(srcStorePath) > 0 {
		srcBlobProvider = compose.NewStoreBlobProvider(compose.GetBlobsRootFor(srcStorePath))
	} else if local {
		// Use the local store as the source blob provider to check whether app is fetched without a need in connection
		// to Registry. Requires app manifest and app archive presence in the local store, otherwise fails.
		srcBlobProvider = store
	} else {
		authorizer := compose.NewRegistryAuthorizer(config.DockerCfg, config.ConnectTimeout)
		resolver := compose.NewResolver(authorizer, config.ConnectTimeout)
		srcBlobProvider = compose.NewRemoteBlobProvider(resolver)
	}
	return
}
