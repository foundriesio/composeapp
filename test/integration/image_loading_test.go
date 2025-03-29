package e2e_tests

import (
	"context"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/reference"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/foundriesio/composeapp/pkg/docker"
	f "github.com/foundriesio/composeapp/test/fixtures"
	"os"
	"path"
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
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	layersRoot := path.Join(f.AppStoreRoot, "blobs", "sha256")

	appImages := make(docker.ImageDescriptions)
	blobProvider := compose.NewStoreBlobProvider(layersRoot)
	composeApp, err := v1.NewAppLoader().LoadAppTree(context.Background(), blobProvider, platforms.Default(), app.PublishedUri)
	if err != nil {
		t.Fatal(err)
	}
	err = composeApp.GetComposeRoot().Walk(func(node *compose.TreeNode, depth int) error {
		if node.Type == compose.BlobTypeImageManifest {
			appImages[node.Ref()] = node.Descriptor.URLs
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var appImageRefs []string
	for uri := range appImages {
		if ref, err := reference.Parse(uri); err == nil {
			appImageRefs = append(appImageRefs, ref.Locator+":"+(ref.Digest().Encoded())[:7])
		}
	}

	err = loadImages(t, context.Background(), cli, appImages, appImageRefs, layersRoot)
	if err != nil {
		t.Fatal(err)
	}
	err = loadImages(t, context.Background(), cli, appImages, appImageRefs, layersRoot,
		docker.WithBlobReadingFromStore(), docker.WithRefWithDigest())
	if err != nil {
		t.Fatal(err)
	}
}

func loadImages(t *testing.T, ctx context.Context, cli *client.Client, appImages docker.ImageDescriptions, appImageRefs []string, layersRoot string, opts ...docker.LoadImageOption) error {
	err := docker.LoadImages(ctx, cli, appImages, layersRoot, opts...)
	if err != nil {
		t.Fatal(err)
	}

	dockerImages, err := cli.ImageList(context.Background(), types.ImageListOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	var dockerImageMap = make(map[string]string)
	for _, di := range dockerImages {
		for _, tag := range di.RepoTags {
			dockerImageMap[tag] = di.ID
		}
	}
	defer func() {
		for _, i := range appImageRefs {
			_, err = cli.ImageRemove(context.Background(), dockerImageMap[i], types.ImageRemoveOptions{Force: true})
			if err != nil {
				t.Fatal(err)
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

func TestAppInstallation(t *testing.T) {
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

	layersRoot := path.Join(f.AppStoreRoot, "blobs", "sha256")
	composeRoot := "/var/sota/compose-apps"

	blobProvider := compose.NewStoreBlobProvider(layersRoot)
	composeApp, err := v1.NewAppLoader().LoadAppTree(context.Background(), blobProvider, platforms.Default(), app.PublishedUri)
	if err != nil {
		t.Fatal(err)
	}

	err = compose.Install(context.Background(), composeApp, blobProvider, layersRoot, composeRoot, "",
		compose.WithInstallProgress(func(p *compose.InstallProgress) {
			if p.AppID != app.PublishedUri {
				t.Fatalf("expected app ID %s, got %s", app.PublishedUri, p.AppID)
			}
			// TODO: check progress
		}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		os.RemoveAll(composeRoot)
		cli, err := compose.GetDockerClient("")
		if err != nil {
			t.Fatal(err)
		}
		_, err = cli.ImagesPrune(context.Background(), filters.NewArgs(filters.Arg("dangling", "false")))
		if err != nil {
			t.Fatal(err)
		}
	}()
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

	layersRoot := path.Join(f.AppStoreRoot, "blobs", "sha256")
	composeRoot := "/var/sota/compose-apps"

	blobProvider := compose.NewStoreBlobProvider(layersRoot)
	composeApp, err := v1.NewAppLoader().LoadAppTree(context.Background(), blobProvider, platforms.Default(), app.PublishedUri)
	if err != nil {
		t.Fatal(err)
	}

	err = compose.Install(context.Background(), composeApp, blobProvider, layersRoot, composeRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		os.RemoveAll(composeRoot)
		cli, err := compose.GetDockerClient("")
		if err != nil {
			t.Fatal(err)
		}
		_, err = cli.ImagesPrune(context.Background(), filters.NewArgs(filters.Arg("dangling", "false")))
		if err != nil {
			t.Fatal(err)
		}
	}()
}
