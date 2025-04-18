package update

import (
	"context"
	"fmt"
	"math"
	"os"
	"path"
)

func (u *runnerImpl) cancel(ctx context.Context) (err error) {
	var errBlobs []string
	progressStep := int(math.Round(100 / float64(len(u.Blobs))))
	for _, b := range u.Blobs {
		p := path.Join(u.config.StoreRoot, "blobs", "sha256", b.Descriptor.Digest.Encoded())
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
	// Should we allow canceling update if it has been installed, if so, then:
	// 1. add image uninstalling from the docker store
	// 2. add app compose project removing from the compose/project dir
	if len(errBlobs) > 0 {
		err = fmt.Errorf("failed to remove blobs; number: %d", len(errBlobs))
	}
	return err
}
