package v1

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/reference"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/jsonmessage"
	units "github.com/docker/go-units"
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
	if checkErrMap, err := app.CheckComposeInstallation(ctx, provider, path.Join(composeRoot, app.Name())); err != nil {
		return err
	} else if len(checkErrMap) > 0 {
		fmt.Println("the following app bundle files are not correctly installed")
		for filePath, fileErr := range checkErrMap {
			fmt.Printf("\t%s\t%s\n", filePath, fileErr)
		}
		return fmt.Errorf("app bundle is not installed completely")
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
		if images.IsIndexType(imageRoot.Descriptor.MediaType) || imageRoot.Type == compose.BlobTypeSkopeoImageIndex {
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
	return loadImagesToDockerWithFallback(ctx, lm, dockerHost)
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
	appContext := app.(*appCtx)
	rc, err := provider.GetReadCloser(compose.WithBlobType(compose.WithAppRef(ctx, &appContext.AppRef), compose.BlobTypeAppBundle),
		compose.WithExpectedDigest(composeDesc.Digest), compose.WithExpectedSize(composeDesc.Size))
	if err != nil {
		return err
	}
	defer rc.Close()

	if err := archive.Untar(rc, path.Join(composeRoot, app.Name()), &tarOptions); err != nil {
		return err
	}
	return nil
}

func loadImagesToDockerWithFallback(ctx context.Context, lm []imageLoadManifest, dockerHost string) error {
	if err := loadImagesToDocker(ctx, lm, nil, dockerHost); err == nil {
		return nil
	} else {
		fmt.Printf("Failed to load images: %s\n", err.Error())
		fmt.Println("Trying the legacy image loading...")
	}
	// a set of blobs (union) across all images to be loaded to dockerd
	blobs := make(map[string]bool)
	for ii := 0; ii < len(lm); ii++ {
		// remove the "digest/hash tag" since the unpatched docker doesn't support it
		lm[ii].RepoTags = []string{lm[ii].RepoTags[1]}
		blobs[path.Join(lm[ii].LayersRoot, lm[ii].Config)] = true
		for _, l := range lm[ii].Layers {
			blobs[path.Join(lm[ii].LayersRoot, l)] = true
		}
	}
	return loadImagesToDocker(ctx, lm, blobs, dockerHost)
}

func processAndPrintImageLoadProgress(in io.Reader, lm []imageLoadManifest) error {
	dec := json.NewDecoder(in)
	curLayerID := ""
	curLayerStatus := 0
	curImgIndx := 0

	for {
		var jm jsonmessage.JSONMessage
		if err := dec.Decode(&jm); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if jm.Error != nil {
			return fmt.Errorf("dockerd err: %s", jm.Error)
		}
		if jm.Progress == nil {
			if curLayerStatus == 2 {
				fmt.Println("done")
				fmt.Printf("Image loaded: %s\n", lm[curImgIndx].RepoTags[0])
			} else {
				fmt.Printf("Image exists: %s\n", lm[curImgIndx].RepoTags[0])
			}
			curLayerID = ""
			curLayerStatus = 0
			curImgIndx += 1
			continue
		}
		if curLayerStatus == 0 {
			// loading new image
			fmt.Printf("Loading image layers: %s\n", lm[curImgIndx].RepoTags[0])
		}
		if curLayerID != jm.ID {
			if curLayerStatus == 2 {
				fmt.Println("done")
			}
			// start of a new layer load
			fmt.Printf("\tID: %s, size: %s:", jm.ID, units.BytesSize(float64(jm.Progress.Total)))
			curLayerID = jm.ID
			curLayerStatus = 1 // layer loading - extracting layer
		}
		if jm.Progress.Current == jm.Progress.Total && curLayerStatus == 1 {
			fmt.Printf(".loaded; syncing...")
			curLayerStatus = 2 // FS syncing the extracted layer
		}
		fmt.Printf(".")
	}
	return nil
}

func loadImagesToDocker(ctx context.Context, lm []imageLoadManifest, blobs map[string]bool, dockerHost string) error {
	manifest, err := json.Marshal(lm)
	if err != nil {
		return err
	}
	tarStreamReader, tarStreamWriter := io.Pipe()
	defer tarStreamReader.Close()
	tarErr := make(chan error, 1)
	go func() {
		defer tarStreamWriter.Close()
		tarErr <- writeToTarStream(manifest, blobs, tarStreamWriter)
	}()
	cli, err := compose.GetDockerClient(dockerHost)
	if err != nil {
		return err
	}
	fmt.Printf("Loading images to the docker engine listening on: %s\n", cli.DaemonHost())
	resp, err := cli.ImageLoad(ctx, tarStreamReader, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := <-tarErr; err != nil {
		return err
	}
	return processAndPrintImageLoadProgress(resp.Body, lm)
}

func writeToTarStream(manifest []byte, blobs map[string]bool, w io.Writer) error {
	tw := tar.NewWriter(w)
	defer tw.Close()
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "manifest.json",
		Size:     int64(len(manifest)),
	}); err != nil {
		return err
	}
	n, err := io.Copy(tw, bytes.NewReader(manifest))
	if err != nil {
		return err
	}
	if n != int64(len(manifest)) {
		return fmt.Errorf("failed to write required number of bytes to tarrer; required: %d, written: %d", len(manifest), n)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	// send all blobs to dockerd through the tar channel if any (legacy mode)
	for b := range blobs {
		fi, err := os.Stat(b)
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		hdr.Name = fi.Name()
		hdr.Format = tar.FormatPAX
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(b)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		if err != nil {
			f.Close()
			return err
		}
		tw.Flush()
		f.Close()
	}
	return nil
}
