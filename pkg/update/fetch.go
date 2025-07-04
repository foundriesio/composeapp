package update

import (
	"context"
	"fmt"
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/opencontainers/go-digest"
)

func (u *runnerImpl) fetch(
	ctx context.Context,
	b *session,
	options ...compose.FetchOption) (err error) {

	opts := compose.FetchOptions{}
	for _, o := range options {
		o(&opts)
	}

	blobsToFetch := make(map[digest.Digest]*compose.BlobInfo)
	for ref, b := range u.Blobs {
		d := digest.FromString(ref)
		blobsToFetch[d] = b
	}
	fetchOptions := options
	// override the progress reporter if one is provided
	fetchOptions = append(fetchOptions,
		compose.WithFetchProgress(func(p *compose.FetchProgress) {
			u.FetchedBytes = p.CurrentBytes
			u.FetchedBlobs = p.FetchedCount
			if u.TotalBlobsBytes != 0 {
				u.Progress = int((p.CurrentBytes * 100) / u.TotalBlobsBytes)
			} else {
				u.Progress = 100
			}
			if u.Progress == 100 {
				u.State = StateFetched
			}
			if storeErr := b.write(&u.Update); storeErr != nil {
				// TODO: replace it by using logger
				fmt.Printf("failed to save update state: %v", storeErr)
			}
			// invoke the progress reporter if one is provided by a caller
			if opts.ProgressHandler != nil {
				opts.ProgressHandler(p)
			}
		}))

	return compose.FetchBlobs(ctx, u.config, blobsToFetch, fetchOptions...)
}
