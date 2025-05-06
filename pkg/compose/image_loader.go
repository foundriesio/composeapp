package compose

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"github.com/distribution/reference"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/foundriesio/composeapp/internal/progress"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"io"
	"os"
	"path"
)

type (
	// ImageDescriptions is a map of image canonical URIs (hostname and digest) to their references.
	ImageDescriptions map[string][]string

	ImageLoadState string

	LoadImageProgress struct {
		State   ImageLoadState
		ImageID string
		ID      string
		Current int64
		Total   int64
	}

	ProgressCallbackFn func(*LoadImageProgress)

	LoadImageOptions struct {
		ReadBlobsFromStore bool
		RefWithDigest      bool
		ProgressReporter   progress.Reporter[LoadImageProgress]
		ProgressCallback   ProgressCallbackFn
	}

	LoadImageOption func(*LoadImageOptions)

	// imageLoadManifest is the manifest of an image load operation.
	// Unfortunately, this struct is not exported by the docker/moby package, and
	// is defined only as an internal type (`manifestItem`) in the docker/moby package,
	// specifically in the `tarexport` package,
	// see https://github.com/moby/moby/image/tarexport/tarexport.go for more details.
	//
	// This struct data are supposed to be transferred as the first part of the tar archive
	// streamed as the request body of the "HTTP POST /images/load" request from the client to the docker server.
	// See https://docs.docker.com/reference/api/engine/version/v1.48/#tag/Image/operation/ImageLoad for more details.
	imageLoadManifest struct {
		// Sha256 hash of the image configuration file
		Config string
		// Image references that include:
		// 1) the image name and tag, e.g., "ubuntu:latest" or "registry:443/myrepo/myimage:mytag",
		// 2) the image name and sha256 hash, e.g.
		//    "registry:5000/factory/rpfck@sha256:6784ff5cbe8f587f42631359081df1629103bf94f3e8662ec202e68e2bcbbcfb"),
		// The second format is supported only if the docker server is patched with the following:
		//    https://github.com/foundriesio/moby/commit/c2594c11b1f2c9b9d26787d3fb190568e6f24f2f
		RepoTags []string
		// Sha256 hashes of the image layers/blobs
		Layers []string
		// The root directory of the image layers/blobs including the image configuration file
		// The vanilla docker server does not support this field and all image layers must be transferred
		// as part of the tar archive streamed as the request body of the "HTTP POST /images/load" request.
		LayersRoot string `json:",omitempty"`
	}

	tarStreamer struct {
		tw *tar.Writer
		r  *io.PipeReader
		w  *io.PipeWriter
	}
)

const (
	ImageLoadStateImageWaiting ImageLoadState = "image-load:image:waiting"
	ImageLoadStateLayerLoading ImageLoadState = "image-load:layer:loading"
	ImageLoadStateLayerSyncing ImageLoadState = "image-load:layer:syncing"
	ImageLoadStateLayerLoaded  ImageLoadState = "image-load:layer:loaded"
	ImageLoadStateImageLoaded  ImageLoadState = "image-load:image:loaded"
	ImageLoadStateImageExist   ImageLoadState = "image-load:image:exist"
)

func WithProgressReporting(pc ProgressCallbackFn) LoadImageOption {
	return func(options *LoadImageOptions) {
		options.ProgressReporter = progress.NewReporter[LoadImageProgress](10)
		options.ProgressCallback = pc
	}
}

func WithBlobReadingFromStore() LoadImageOption {
	return func(options *LoadImageOptions) {
		options.ReadBlobsFromStore = true
	}
}
func WithRefWithDigest() LoadImageOption {
	return func(options *LoadImageOptions) {
		options.RefWithDigest = true
	}
}

// LoadImages loads images to the docker store by making `HTTP POST /images/load` request
// to the docker server using the docker client.
// The request body is a tar archive that contains:
// 1) the image load manifests (`[]imageLoadManifest`) - must be the first file in the tar named `manifest.json`,
// 2) optionally the image layers/blobs.
//
// E.g., `curl --unix-socket /var/run/docker.sock -X POST -H "Content-Type: application/x-tar"
//			   --data-binary @image-load.tar http://localhost/images/load`
//
// If `LoadImageOptions.ReadBlobsFromStore` is not set then all layers must be streamed through the request body.
// In this case, layers will be streamed to the docker server which at first stores them in its temporary directory,
// and later loads the layers from the temporary directory to the docker store. Therefore, the layers are copied twice.
//
// If `LoadImageOptions.ReadBlobsFromStore` is set then the docker server loads the image layers directly
// from the directory specified  via the `blobsRoot` parameter, thus, the layers are copied only once.
// To support this feature, the docker server must be patched with the following patch:
// https://github.com/foundriesio/moby/commit/c2594c11b1f2c9b9d26787d3fb190568e6f24f2f,
// otherwise the image load will fail.
//
// If `LoadImageOptions.RefWithDigest` is set then the docker store will be populated with the digest references
// to the loaded images in addition to the tag references. It helps to avoid a need in requesting container registry
// when a docker client pulls an image by digest reference.
// The docker server must be patched with the following patch to support this feature:
// https://github.com/foundriesio/moby/commit/c2594c11b1f2c9b9d26787d3fb190568e6f24f2f,

func LoadImages(ctx context.Context,
	client *client.Client,
	images ImageDescriptions,
	blobsRoot string,
	opts ...LoadImageOption) error {

	options := &LoadImageOptions{}
	for _, opt := range opts {
		opt(options)
	}

	if options.ProgressReporter != nil {
		options.ProgressReporter.Start(options.ProgressCallback)
		defer options.ProgressReporter.Stop(true)
	}

	layersMap := make(map[string]v1.Descriptor)
	var imageLoadManifests []*imageLoadManifest
	var imageURIs []string
	// Generate the image load manifests
	for uri, refs := range images {
		// Generate the image load manifest
		manifest, err := generateImageLoadManifest(uri, refs, blobsRoot, layersMap, options)
		if err != nil {
			return err
		}
		imageLoadManifests = append(imageLoadManifests, manifest)
		imageURIs = append(imageURIs, uri)
	}

	var blobPaths []string
	if !options.ReadBlobsFromStore {
		// A set of blobs (union) across all images to be loaded to the docker store
		blobs := make(map[string]bool)
		for _, m := range imageLoadManifests {
			// If `LayersRoot` is empty then all image layers must be streamed in the request body.
			blobs[path.Join(blobsRoot, m.Config)] = true
			for _, l := range m.Layers {
				blobs[path.Join(blobsRoot, l)] = true
			}
		}

		// Convert the `blobs` map to a slice of strings
		for p := range blobs {
			blobPaths = append(blobPaths, p)
		}
	}

	// Serialize the image load manifests to a byte slice
	manifestData, err := json.Marshal(imageLoadManifests)
	if err != nil {
		return err
	}

	// Create a tar streamer
	ts := NewTarStreamer()

	// Write the image load manifests and blobs to the tar streamer in parallel.
	// Report any error that occurs during the write operation through the `errCh` channel.
	errCh := make(chan error, 1)
	go func() {
		defer ts.Close()
		err := ts.WriteFileData(manifestData, "manifest.json")
		if err != nil {
			errCh <- err
			return
		}
		if !options.ReadBlobsFromStore {
			err = ts.WriteFiles(blobPaths)
			if err != nil {
				errCh <- err
				return
			}
		}
	}()

	r, err := client.ImageLoad(ctx, ts.Reader(), false)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	// TODO: Read and process the image load progress messages from the response body `r.Body`
	dec := json.NewDecoder(r.Body)

	curImageIndex := 0
	curLayerID := ""
	p := &LoadImageProgress{
		State:   ImageLoadStateImageWaiting,
		ImageID: imageURIs[curImageIndex],
	}

	var jm jsonmessage.JSONMessage

	for {
		// Decode the next message from the response body
		if decodeErr := dec.Decode(&jm); decodeErr != nil {
			if decodeErr != io.EOF {
				// An error occurred while decoding the message, except for EOF
				err = decodeErr
			}
			break
		}

		switch p.State {
		case ImageLoadStateImageWaiting:
			{
				if jm.Progress == nil {
					p.State = ImageLoadStateImageExist
					reportProgressIfEnabled(options, p)

					curImageIndex++
					curLayerID = ""
					p.State = ImageLoadStateImageWaiting
				} else {
					curLayerID = jm.ID
					p.ImageID = imageURIs[curImageIndex]
					if _, ok := layersMap[curLayerID]; ok {
						p.ID = layersMap[curLayerID].Digest.Encoded()[:7]
					} else {
						p.ID = "unknown"
					}
					p.State = ImageLoadStateLayerLoading
					p.Current = jm.Progress.Current
					p.Total = jm.Progress.Total
					reportProgressIfEnabled(options, p)
				}
			}
		case ImageLoadStateLayerLoading:
			{
				p.Current = jm.Progress.Current
				p.Total = jm.Progress.Total
				reportProgressIfEnabled(options, p)
				if jm.Progress.Current == jm.Progress.Total {
					// Image layer files were extracted and written to filesystem, now fsyncing has started
					// Unfortunately, we cannot track progress of fsyncing, so we just report the state
					p.State = ImageLoadStateLayerSyncing
					reportProgressIfEnabled(options, p)
				}
			}
		case ImageLoadStateLayerSyncing:
			{
				if jm.Progress == nil {
					p.State = ImageLoadStateImageLoaded
					reportProgressIfEnabled(options, p)

					curImageIndex++
					curLayerID = ""
					p.State = ImageLoadStateImageWaiting
					if curImageIndex < len(imageURIs) {
						p.ImageID = imageURIs[curImageIndex]
					}
				} else if curLayerID != jm.ID {
					p.State = ImageLoadStateLayerLoaded
					reportProgressIfEnabled(options, p)

					// Start new image layer loading
					curLayerID = jm.ID
					if _, ok := layersMap[curLayerID]; ok {
						p.ID = layersMap[curLayerID].Digest.Encoded()[:7]
					} else {
						p.ID = "unknown"
					}
					p.State = ImageLoadStateLayerLoading

					p.Current = jm.Progress.Current
					p.Total = jm.Progress.Total
					reportProgressIfEnabled(options, p)
				}
			}
		}
	}

	return err
}

func generateImageLoadManifest(
	imageURI string,
	imageRefs []string,
	imageBlobsRootDir string,
	layersMap map[string]v1.Descriptor,
	options *LoadImageOptions) (*imageLoadManifest, error) {
	var imageRef reference.Canonical
	var imageTaggedRef string
	if ref, err := reference.Parse(imageURI); err == nil {
		if canonicalRef, ok := ref.(reference.Canonical); ok {
			imageRef = canonicalRef
			imageTaggedRef = canonicalRef.Name() + ":" + canonicalRef.Digest().Encoded()[:7]
		} else {
			return nil, fmt.Errorf("the specified image reference does not contain"+
				" registry domain or digest): %s", imageURI)
		}
	} else {
		return nil, err
	}

	imageManifestPath := path.Join(imageBlobsRootDir, imageRef.Digest().Encoded())
	b, err := os.ReadFile(imageManifestPath)
	if err != nil {
		return nil, err
	}

	var imageManifest v1.Manifest
	err = json.Unmarshal(b, &imageManifest)
	if err != nil {
		return nil, err
	}

	imageConfigManifestPath := path.Join(imageBlobsRootDir, imageManifest.Config.Digest.Encoded())
	b, err = os.ReadFile(imageConfigManifestPath)
	if err != nil {
		return nil, err
	}
	var imageConfig v1.Image
	err = json.Unmarshal(b, &imageConfig)
	if err != nil {
		return nil, err
	}

	loadManifest := &imageLoadManifest{
		Config:   imageManifest.Config.Digest.Encoded(),
		RepoTags: []string{imageTaggedRef},
	}
	for ind, l := range imageManifest.Layers {
		loadManifest.Layers = append(loadManifest.Layers, l.Digest.Encoded())
		// map the layer's diff ID to the layer descriptor
		layersMap[imageConfig.RootFS.DiffIDs[ind].Encoded()[:12]] = l
	}
	if options.ReadBlobsFromStore {
		loadManifest.LayersRoot = imageBlobsRootDir
	}
	for _, r := range imageRefs {
		ref, err := reference.Parse(r)
		if err != nil {
			return nil, err
		}
		switch ref.(type) {
		case reference.NamedTagged:
			if imageTaggedRef != ref.String() {
				loadManifest.RepoTags = append(loadManifest.RepoTags, ref.String())
			}
		case reference.Digested:
			if options.RefWithDigest {
				loadManifest.RepoTags = append(loadManifest.RepoTags, ref.String())
			}
		default:
			return nil, fmt.Errorf("unsupported image reference type: %s", r)
		}
	}
	return loadManifest, err
}

func reportProgressIfEnabled(opts *LoadImageOptions, p *LoadImageProgress) {
	if opts.ProgressReporter == nil {
		return
	}

	// TODO: check whether the progress message was dropped and print log message if so
	opts.ProgressReporter.Update(*p)
}
