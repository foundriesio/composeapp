package v1

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/reference"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
	"github.com/foundriesio/composeapp/pkg/compose"
	"io"
	"os"
	"path"
)

type (
	imageLoadManifest struct {
		Config     string
		RepoTags   []string
		Layers     []string
		LayersRoot string
	}
)

func InstallApp(ctx context.Context, app compose.App, provider compose.BlobProvider, blobRoot string, composeRoot string, dockerHost string) error {
	if err := installCompose(ctx, app, provider, composeRoot); err != nil {
		return err
	}
	composeTreeRoot := app.GetComposeRoot()
	if composeTreeRoot == nil {
		return fmt.Errorf("failed to get app's compose project")
	}
	var lm []imageLoadManifest
	for _, imageRoot := range composeTreeRoot.Children {
		imageUri := imageRoot.Descriptor.URLs[0]
		tags := []string{imageUri}
		if s, err := reference.Parse(imageUri); err == nil {
			tags = append(tags, s.Locator+":"+(s.Digest().Encoded())[:7])
		}
		manifestNode := imageRoot
		if images.IsIndexType(imageRoot.Descriptor.MediaType) {
			manifestNode = imageRoot.Children[0]
		}
		var lh []string
		var config string
		for _, child := range manifestNode.Children {
			if images.IsConfigType(child.Descriptor.MediaType) {
				config = child.Descriptor.Digest.Encoded()
			} else if images.IsLayerType(child.Descriptor.MediaType) {
				lh = append(lh, child.Descriptor.Digest.Encoded())
			} else {
				return fmt.Errorf("invalid image manifest child media type: %s", child.Descriptor.MediaType)
			}
		}

		lm = append(lm, imageLoadManifest{
			Config:     config,
			RepoTags:   tags,
			Layers:     lh,
			LayersRoot: blobRoot,
		})
	}
	return loadImagesToDocker(ctx, lm, dockerHost)
}

func installCompose(ctx context.Context, app compose.App, provider compose.BlobProvider, composeRoot string) error {
	appInstallDir := path.Join(composeRoot, app.Name())
	if err := os.MkdirAll(appInstallDir, 0755); err != nil {
		return err
	}
	tarOptions := archive.TarOptions{
		NoLchown: true,
	}
	composeDesc, err := app.(*appCtx).GetComposeDescriptor()
	if err != nil {
		return err
	}

	rc, err := provider.GetReadCloser(ctx, compose.WithExpectedDigest(composeDesc.Digest), compose.WithExpectedSize(composeDesc.Size))
	if err != nil {
		return err
	}
	defer rc.Close()

	if err := archive.Untar(rc, path.Join(composeRoot, app.Name()), &tarOptions); err != nil {
		return err
	}
	return nil
}

func loadImagesToDocker(ctx context.Context, lm []imageLoadManifest, dockerHost string) error {
	// TODO: support two types of image load, the regular load that does not require the docker patch, and
	// the given one that requires the patch. The problem with the first one is that it requires transferring blobs
	// via the tar stream (what's point in the copying them if ther are already present in the local system?).
	b, err := json.Marshal(lm)
	if err != nil {
		return err
	}
	var tarredLoadManifest bytes.Buffer
	tw := tar.NewWriter(&tarredLoadManifest)
	defer tw.Close()
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "manifest.json",
		Size:     int64(len(b)),
	}); err != nil {
		return err
	}
	n, err := io.Copy(tw, bytes.NewReader(b))
	if err != nil {
		return err
	}
	if n != int64(len(b)) {
		return fmt.Errorf("failed to write required number of bytes to tarrer; required: %d, written: %d", len(b), n)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("failed to tar image load manifest: %s", err.Error())
	}

	opts := []dockerclient.Opt{
		dockerclient.FromEnv,
	}
	if len(dockerHost) > 0 {
		opts = append(opts, dockerclient.WithHost(dockerHost))
	}
	cli, err := dockerclient.NewClientWithOpts(opts...)
	if err != nil {
		return err
	}
	fmt.Printf("Loading images to the docker engine listening on: %s\n", cli.DaemonHost())
	resp, err := cli.ImageLoad(ctx, &tarredLoadManifest, true)
	if err != nil {
		return fmt.Errorf("failed to load images to docker: %s", err.Error())
	}
	if b, err := io.ReadAll(resp.Body); err == nil {
		//TODO: pretty print of both error and success
		fmt.Printf("%s\n", string(b))
	} else {
		fmt.Printf("Failed to read response to the image load request: %s\n", err.Error())
	}
	return nil
}
