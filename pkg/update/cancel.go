package update

import (
	"context"
	"fmt"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/foundriesio/composeapp/pkg/compose"
	"math"
	"os"
	"path"
)

func (u *runnerImpl) cancel(ctx context.Context) (err error) {

	if len(u.LoadedImages) > 0 {
		cli, err := compose.GetDockerClient(u.config.DockerHost)
		if err != nil {
			return err
		}
		imageTags := make(map[string]struct{})
		for imageURI := range u.LoadedImages {
			if ref, err := compose.ParseImageRef(imageURI); err == nil {
				imageTags[ref.GetTagRef()] = struct{}{}
			}
		}
		if err := removeLoadedImages(ctx, cli, imageTags); err != nil {
			return err
		}
	}

	// TODO: remove any installed compose projects

	var errBlobs []string
	progressStep := int(math.Round(100 / float64(len(u.Blobs))))
	for _, b := range u.Blobs {
		p := path.Join(u.config.GetBlobsRoot(), b.Descriptor.Digest.Encoded())
		if err := os.Remove(p); err != nil {
			if !os.IsNotExist(err) {
				// TODO: add debug logging
				errBlobs = append(errBlobs, b.Descriptor.Digest.Encoded())
			}
		}
		// take into account the rounding error
		if u.Progress < 100 {
			u.Progress += progressStep
			if u.Progress > 100 {
				u.Progress = 100
			}
		}
	}
	if len(errBlobs) > 0 {
		err = fmt.Errorf("failed to remove blobs; number: %d", len(errBlobs))
	}
	return err
}

func removeLoadedImages(ctx context.Context, cli *client.Client, imageTags map[string]struct{}) error {
	images, err := cli.ImageList(ctx, dockertypes.ImageListOptions{All: true})
	if err != nil {
		return err
	}
	for _, image := range images {
		for _, imageTag := range image.RepoTags {
			if _, ok := imageTags[imageTag]; ok {
				_, err = cli.ImageRemove(ctx, image.ID, dockertypes.ImageRemoveOptions{Force: true})
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}
