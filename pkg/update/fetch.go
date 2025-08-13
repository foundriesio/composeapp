package update

import (
	"context"
	"fmt"
	"github.com/foundriesio/composeapp/pkg/compose"
)

func (u *runnerImpl) fetch(
	ctx context.Context,
	b *session,
	options ...compose.FetchOption) (err error) {

	opts := compose.FetchOptions{}
	for _, o := range options {
		o(&opts)
	}

	fetchOptions := options
	// override the progress reporter if one is provided
	fetchOptions = append(fetchOptions,
		compose.WithFetchProgress(func(p *compose.FetchProgress) {
			for d, b := range p.Blobs {
				if u.Blobs[d].State == compose.BlobOk {
					// if the blob is already fetched, skip it
					continue
				}
				// update the blob state in the session, bytes fetched and state
				u.Blobs[d].BytesFetched = b.BytesFetched
				u.Blobs[d].State = b.State
			}
			// update the overall update state, overall fetched bytes and the number of blobs fetched so far
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

	blobsToFetchInfo := make(compose.BlobsInfo)
	for d, b := range u.Blobs {
		blobsToFetchInfo[d] = &b.BlobInfo
	}
	return compose.FetchBlobs(ctx, u.config, blobsToFetchInfo, fetchOptions...)
}
