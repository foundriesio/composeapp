package compose

import (
	"context"
	"errors"
	"fmt"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/errdefs"
	"github.com/foundriesio/composeapp/internal/progress"
	"github.com/opencontainers/go-digest"
	"sync"
	"time"
)

const (
	DefaultPollInterval = 300 // Default interval between polling/checking blob download status in milliseconds
)

type (
	FetchProgress struct {
		Blobs        map[digest.Digest]*BlobInfo // per-blob metadata and progress
		FetchedCount int                         // number of fully fetched blobs
		CurrentBytes int64                       // total bytes downloaded so far
		TotalBytes   int64                       // total bytes expected to download
	}

	FetchOptions struct {
		ProgressPollInterval int // interval between polling/checking blob download status in milliseconds
		ProgressHandler      FetchProgressFunc
	}

	FetchOption       func(*FetchOptions)
	FetchProgressFunc func(*FetchProgress)
)

func WithFetchProgress(pf FetchProgressFunc) FetchOption {
	return func(o *FetchOptions) {
		o.ProgressHandler = pf
	}
}

func WithProgressPollInterval(pollInterval int) FetchOption {
	return func(opts *FetchOptions) {
		opts.ProgressPollInterval = pollInterval
	}
}

func FetchBlobs(ctx context.Context, cfg *Config, blobsToFetch map[digest.Digest]*BlobInfo, options ...FetchOption) error {
	opts := FetchOptions{}
	for _, o := range options {
		o(&opts)
	}

	var progressReporter progress.Reporter[FetchProgress]

	if opts.ProgressHandler != nil {
		progressReporter = progress.NewReporter[FetchProgress](2)
		progressReporter.Start(opts.ProgressHandler)
	}
	defer func() {
		if progressReporter != nil {
			progressReporter.Stop(ctx.Err() == nil)
		}
	}()

	var totalBlobsFetchSize int64
	for _, blob := range blobsToFetch {
		totalBlobsFetchSize += blob.Descriptor.Size
	}

	authorizer := NewRegistryAuthorizer(cfg.DockerCfg, cfg.ConnectTimeout)
	resolver := NewResolver(authorizer, cfg.ConnectTimeout)
	localStore, err := local.NewStore(cfg.StoreRoot)
	if err != nil {
		return fmt.Errorf("failed to create local store: %w", err)
	}

	var progressWg sync.WaitGroup
	stopChan := make(chan struct{})
	if progressReporter != nil {
		var pollInterval int
		if opts.ProgressPollInterval > 0 {
			pollInterval = opts.ProgressPollInterval
		} else {
			pollInterval = DefaultPollInterval
		}
		progressWg.Add(1)

		go func(stopChan chan struct{}) {
			defer progressWg.Done()
			for {
				select {
				case <-ctx.Done():
					checkAndUpdateBlobStatus(ctx, blobsToFetch, localStore, totalBlobsFetchSize, progressReporter)
					return
				case <-stopChan:
					checkAndUpdateBlobStatus(ctx, blobsToFetch, localStore, totalBlobsFetchSize, progressReporter)
					return
				default:
					checkAndUpdateBlobStatus(ctx, blobsToFetch, localStore, totalBlobsFetchSize, progressReporter)
				}
				time.Sleep(time.Duration(pollInterval) * time.Millisecond)
			}
		}(stopChan)
	}

	var blobsToResumeDownload []*BlobInfo
	var blobsToStartDownload []*BlobInfo

	for _, bi := range blobsToFetch {
		if bi.State == BlobFetching {
			blobsToResumeDownload = append(blobsToResumeDownload, bi)
		} else {
			blobsToStartDownload = append(blobsToStartDownload, bi)
		}
	}

	for _, bi := range blobsToResumeDownload {
		err = CopyBlob(ctx, resolver, bi.Descriptor.URLs[0], *bi.Descriptor, localStore, true)
		if err != nil {
			// TODO: log this error and do retry
			fmt.Printf("failed to fetch blob %store: %v", bi.Descriptor.Digest, err)
			break
		}
	}

	for _, bi := range blobsToStartDownload {
		err = CopyBlob(ctx, resolver, bi.Descriptor.URLs[0], *bi.Descriptor, localStore, true)
		if err != nil {
			// TODO: log this error and do retry
			fmt.Printf("failed to fetch blob %store: %v", bi.Descriptor.Digest, err)
			break
		}
	}

	if progressReporter != nil {
		if ctx.Err() == nil {
			// stop the progress reporter if it wasn't stopped yet through the context cancel
			stopChan <- struct{}{}
		}
		progressWg.Wait()
	}

	return ctx.Err()
}

func checkAndUpdateBlobStatus(ctx context.Context, blobsToFetch map[digest.Digest]*BlobInfo, ls content.Store, totalBytes int64, sr progress.Reporter[FetchProgress]) {
	var currentBytes int64
	var fetchedCount int
	for _, b := range blobsToFetch {
		if s, err := ls.Status(ctx, b.Descriptor.URLs[0]); err == nil {
			currentBytes += s.Offset
			b.BytesFetched = s.Offset
			if b.State != BlobFetching {
				b.State = BlobFetching
			}
			if b.LastFetchStartBytes == 0 {
				b.LastFetchStartBytes = s.Offset
			}
			if b.LastFetchStartTime == (time.Time{}) {
				b.LastFetchStartTime = time.Now()
			}
		} else if errors.Is(err, errdefs.ErrNotFound) {
			if i, err := ls.Info(ctx, b.Descriptor.Digest); err == nil {
				currentBytes += i.Size
				b.BytesFetched = i.Size
				b.State = BlobOk
				fetchedCount++
			}
		}
	}
	sr.Update(FetchProgress{
		Blobs:        blobsToFetch,
		FetchedCount: fetchedCount,
		CurrentBytes: currentBytes,
		TotalBytes:   totalBytes,
	},
	)
}
