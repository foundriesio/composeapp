package e2e_tests

import (
	"context"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	f "github.com/foundriesio/composeapp/test/fixtures"
	"os"
	"testing"
)

func TestAppImageLoading(t *testing.T) {
	appComposeDef := `
services:
  srvs-01:
    image: registry:5000/factory/runner-image:v0.1
    command: sh -c "while true; do sleep 60; done"
    ports:
    - 8080:80
  busybox:
    image: ghcr.io/foundriesio/busybox:1.36
    command: sh -c "while true; do sleep 60; done"
`
	app := f.NewApp(t, appComposeDef)
	app.Publish(t)

	app.Pull(t)
	defer app.Remove(t)
	app.CheckFetched(t)

	cli, err := compose.GetDockerClient("")
	f.Check(t, err)
	defer cli.Close()

	layersRoot := compose.GetBlobsRootFor(f.AppStoreRoot)

	blobProvider := compose.NewStoreBlobProvider(layersRoot)
	composeApp, err := v1.NewAppLoader().LoadAppTree(context.Background(), blobProvider, platforms.Default(), app.PublishedUri)
	f.Check(t, err)

	err = loadImages(t, context.Background(), cli, composeApp, layersRoot)
	f.Check(t, err)
	err = loadImages(t, context.Background(), cli, composeApp, layersRoot,
		compose.WithProgressReporting(progressHandler), compose.WithBlobReadingFromStore(), compose.WithRefWithDigest())
	f.Check(t, err)
}

func progressHandler(progress *compose.LoadImageProgress) {
	fmt.Printf("Progress: ID: %s -> %d/%d\n", progress.ID, progress.Current, progress.Total)
}

func loadImages(t *testing.T, ctx context.Context, cli *client.Client, app compose.App, layersRoot string, options ...compose.LoadImageOption) error {
	opts := &compose.LoadImageOptions{}
	for _, o := range options {
		o(opts)
	}

	err := compose.LoadImages(ctx, cli, app, layersRoot, options...)
	f.Check(t, err)

	dockerImages, err := cli.ImageList(context.Background(), types.ImageListOptions{All: true})
	f.Check(t, err)
	var dockerImageMap = make(map[string]string)
	for _, di := range dockerImages {
		for _, tag := range di.RepoTags {
			dockerImageMap[tag] = di.ID
		}
		for _, d := range di.RepoDigests {
			dockerImageMap[d] = di.ID
		}
	}

	var appImageRefs []string
	for _, imageNode := range app.GetComposeRoot().Children {
		imageRoot := imageNode
		for {
			imageRef, err := compose.ParseImageRef(imageRoot.Ref())
			f.Check(t, err)
			appImageRefs = append(appImageRefs, imageRef.GetTagRef())
			if opts.RefWithDigest {
				appImageRefs = append(appImageRefs, imageRoot.Ref())
			}
			if imageRoot.Type == compose.BlobTypeImageManifest {
				break
			}
			if !(imageRoot.Type == compose.BlobTypeImageIndex || imageRoot.Type == compose.BlobTypeSkopeoImageIndex) {
				t.Fatalf("invalid image type is specified: %s", imageRoot.Type)
			}
			if len(imageRoot.Children) != 1 {
				t.Fatalf("the specified image index has more than one manifest: %s", imageRoot.Ref())
			}
			imageRoot = imageRoot.Children[0]
		}
	}

	defer func() {
		deletedImages := make(map[string]struct{})
		for _, i := range appImageRefs {
			imageID, ok := dockerImageMap[i]
			if !ok {
				continue
			}
			if _, ok := deletedImages[imageID]; !ok {
				_, err = cli.ImageRemove(context.Background(), imageID, types.ImageRemoveOptions{Force: true})
				if err != nil {
					t.Fatal(err)
				}
				deletedImages[imageID] = struct{}{}
			}
		}
	}()

	for _, i := range appImageRefs {
		if len(dockerImageMap[i]) == 0 {
			t.Fatalf("Image %s not loaded", i)
		}
	}
	return nil
}

func TestAppInstallationWithProgress(t *testing.T) {
	appComposeDef := `
services:
  srvs-01:
    image: registry:5000/factory/runner-image:v0.1
    command: sh -c "while true; do sleep 60; done"
    ports:
    - 8080:80
  busybox:
    image: ghcr.io/foundriesio/busybox:1.36
    command: sh -c "while true; do sleep 60; done"
`
	app := f.NewApp(t, appComposeDef)
	app.Publish(t)

	app.Pull(t)
	defer app.Remove(t)
	app.CheckFetched(t)

	cfg := f.NewTestConfig(t)
	err := compose.Install(context.Background(), cfg, app.PublishedUri,
		compose.WithInstallProgress(func(p *compose.InstallProgress) {
			if p.AppID != app.PublishedUri {
				t.Fatalf("expected app ID %s, got %s", app.PublishedUri, p.AppID)
			}
			// TODO: check progress
		}))
	f.Check(t, err)
	defer func() {
		os.RemoveAll(cfg.ComposeRoot)
		cli, err := compose.GetDockerClient("")
		f.Check(t, err)
		_, err = cli.ImagesPrune(context.Background(), filters.NewArgs(filters.Arg("dangling", "false")))
		f.Check(t, err)
	}()
}

func TestAppInstallationWithoutProgress(t *testing.T) {
	appComposeDef := `
services:
  srvs-01:
    image: registry:5000/factory/runner-image:v0.1
    command: sh -c "while true; do sleep 60; done"
    ports:
    - 8080:80
  busybox:
    image: ghcr.io/foundriesio/busybox:1.36
    command: sh -c "while true; do sleep 60; done"
`
	app := f.NewApp(t, appComposeDef)
	app.Publish(t)

	app.Pull(t)
	defer app.Remove(t)
	app.CheckFetched(t)

	cfg := f.NewTestConfig(t)
	err := compose.Install(context.Background(), cfg, app.PublishedUri)
	f.Check(t, err)
	defer func() {
		os.RemoveAll(cfg.ComposeRoot)
		cli, err := compose.GetDockerClient("")
		f.Check(t, err)
		_, err = cli.ImagesPrune(context.Background(), filters.NewArgs(filters.Arg("dangling", "false")))
		f.Check(t, err)
	}()
}
