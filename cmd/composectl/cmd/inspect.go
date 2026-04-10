package composectl

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/containerd/containerd/platforms"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/spf13/cobra"
)

type inspectOptions struct {
	Format string
}

func init() {
	inspectCmd := &cobra.Command{
		Use:   "inspect <app ref>",
		Short: "inspect <ref>",
		Long:  ``,
		Args:  cobra.ExactArgs(1),
	}
	opts := inspectOptions{}
	inspectCmd.Flags().StringVar(&opts.Format, "format", "plain",
		"Output format; supported: plain, json")
	inspectCmd.Run = func(cmd *cobra.Command, args []string) {
		if opts.Format != "plain" && opts.Format != "json" {
			DieNotNil(cmd.Usage())
			fmt.Fprintf(os.Stderr, "unsupported  `--format` value: %s\n", opts.Format)
			os.Exit(1)
		}
		inspectApp(cmd, args, &opts)
	}
	rootCmd.AddCommand(inspectCmd)
}

func inspectApp(cmd *cobra.Command, args []string, opts *inspectOptions) {
	appRef := args[0]

	if opts.Format == "plain" {
		fmt.Printf("Inspecting App %s...", appRef)
	}
	app, err := v1.NewAppLoader().LoadAppTree(cmd.Context(), compose.NewRemoteBlobProviderFromConfig(config), platforms.All, appRef)
	DieNotNil(err)
	if opts.Format == "plain" {
		fmt.Println("ok")
		app.Tree().Print()
	} else {
		// json
		type BlobInfo struct {
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
		}

		type ManifestInfo struct {
			BlobInfo
			Architecture string     `json:"architecture,omitempty"`
			Config       BlobInfo   `json:"config"`
			Layers       []BlobInfo `json:"layers"`
		}

		type ImageContentInfo struct {
			Ref       string         `json:"ref"`
			Manifests []ManifestInfo `json:"manifests"`
		}

		type ServiceContentInfo struct {
			Name  string           `json:"name"`
			Image ImageContentInfo `json:"image"`
		}

		type BundleInfo struct {
			BlobInfo
			Services []ServiceContentInfo `json:"services,omitempty"`
		}

		type AppInfo struct {
			Name   string     `json:"name"`
			Ref    string     `json:"ref"`
			Meta   BlobInfo   `json:"meta"`
			Index  BlobInfo   `json:"index"`
			Bundle BundleInfo `json:"bundle"`
		}

		currApp := AppInfo{
			Name: app.Name(),
			Ref:  app.Ref().Spec.String(),
		}
		currApp.Bundle = BundleInfo{
			Services: []ServiceContentInfo{},
		}

		err = app.Tree().Walk(func(node *compose.TreeNode, depth int) error {
			if depth == 1 {
				switch node.Type {
				case compose.BlobTypeAppLayersMeta:
					currApp.Meta = BlobInfo{
						Digest: node.Descriptor.Digest.String(),
						Size:   node.Descriptor.Size,
					}
				case compose.BlobTypeAppIndex:
					currApp.Index = BlobInfo{
						Digest: node.Descriptor.Digest.String(),
						Size:   node.Descriptor.Size,
					}
				case compose.BlobTypeAppBundle:
					currApp.Bundle = BundleInfo{
						BlobInfo: BlobInfo{
							Digest: node.Descriptor.Digest.String(),
							Size:   node.Descriptor.Size,
						},
						Services: []ServiceContentInfo{},
					}
				}
			} else if depth == 2 {
				var manifests []ManifestInfo
				if node.Type == compose.BlobTypeImageManifest {
					manifestInfo := ManifestInfo{
						BlobInfo: BlobInfo{
							Digest: node.Descriptor.Digest.String(),
							Size:   node.Descriptor.Size,
						},
						// If an image manifest is found at depth 2,
						// then there is no platform information available, as the manifest is not part of an image index.
						Architecture: "unknown",
					}
					manifests = append(manifests, manifestInfo)
				}
				serviceInfo := ServiceContentInfo{
					Name: node.Descriptor.Annotations[v1.AnnotationKeyAppServiceName],
					Image: ImageContentInfo{
						Ref:       node.Descriptor.URLs[0],
						Manifests: manifests,
					},
				}
				currApp.Bundle.Services = append(currApp.Bundle.Services, serviceInfo)
			} else if depth == 3 && node.Type == compose.BlobTypeImageManifest {
				manifestInfo := ManifestInfo{
					BlobInfo: BlobInfo{
						Digest: node.Descriptor.Digest.String(),
						Size:   node.Descriptor.Size,
					},
					Architecture: node.Descriptor.Platform.Architecture,
				}
				lastIndex := &currApp.Bundle.Services[len(currApp.Bundle.Services)-1]
				lastIndex.Image.Manifests = append(lastIndex.Image.Manifests, manifestInfo)
			} else if depth == 4 || depth == 3 {
				switch node.Type {
				case compose.BlobTypeImageConfig:
					lastIndex := &currApp.Bundle.Services[len(currApp.Bundle.Services)-1]
					lastIndex.Image.Manifests[len(lastIndex.Image.Manifests)-1].Config = BlobInfo{
						Digest: node.Descriptor.Digest.String(),
						Size:   node.Descriptor.Size,
					}
				case compose.BlobTypeImageLayer:
					lastIndex := &currApp.Bundle.Services[len(currApp.Bundle.Services)-1]
					lastIndex.Image.Manifests[len(lastIndex.Image.Manifests)-1].Layers = append(lastIndex.Image.Manifests[len(lastIndex.Image.Manifests)-1].Layers, BlobInfo{
						Digest: node.Descriptor.Digest.String(),
						Size:   node.Descriptor.Size,
					})
				}
			}
			return nil
		})
		DieNotNil(err)

		b, err := json.Marshal(currApp)
		DieNotNil(err)
		fmt.Println(string(b))
	}
	if opts.Format == "plain" {
		fmt.Printf("App tree node count: %d\n", app.NodeCount())
	}
}
