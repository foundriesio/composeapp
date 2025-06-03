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
		Blobs        []*BlobInfo
		FetchedBlobs int
		Current      int64
		Total        int64
	}

	FetchOptions struct {
		ProgressReporter     progress.Reporter[FetchProgress]
		ProgressPollInterval int // interval between polling/checking blob download status in milliseconds
	}

	FetchOption       func(*FetchOptions)
	FetchProgressFunc func(*FetchProgress)
)

func WithFetchProgress(pf FetchProgressFunc) FetchOption {
	return func(o *FetchOptions) {
		o.ProgressReporter = progress.NewReporter[FetchProgress](2)
		o.ProgressReporter.Start(pf)
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

	defer func() {
		if opts.ProgressReporter != nil {
			opts.ProgressReporter.Stop(ctx.Err() == nil)
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
	if opts.ProgressReporter != nil {
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
					checkAndUpdateBlobStatus(ctx, blobsToFetch, localStore, totalBlobsFetchSize, opts.ProgressReporter)
					return
				case <-stopChan:
					checkAndUpdateBlobStatus(ctx, blobsToFetch, localStore, totalBlobsFetchSize, opts.ProgressReporter)
					return
				default:
					checkAndUpdateBlobStatus(ctx, blobsToFetch, localStore, totalBlobsFetchSize, opts.ProgressReporter)
				}
				time.Sleep(time.Duration(pollInterval) * time.Millisecond)
			}
		}(stopChan)
	}
	for _, bi := range blobsToFetch {
		err = CopyBlob(ctx, resolver, bi.Descriptor.URLs[0], *bi.Descriptor, localStore, true)
		if err != nil {
			// TODO: log this error and do retry
			fmt.Printf("failed to fetch blob %store: %v", bi.Descriptor.Digest, err)
			break
		}
	}

	if opts.ProgressReporter != nil {
		if ctx.Err() == nil {
			// stop the progress reporter if it wasn't stopped yet through the context cancel
			stopChan <- struct{}{}
		}
		progressWg.Wait()
	}

	return ctx.Err()
}

func checkAndUpdateBlobStatus(ctx context.Context, blobsToFetch map[digest.Digest]*BlobInfo, ls content.Store, totalBlobsFetchSize int64, sr progress.Reporter[FetchProgress]) {
	var currentUpdateDownloadSize int64
	var inFlightBlobs []*BlobInfo
	var fetchedBlobs int
	for _, b := range blobsToFetch {
		if s, err := ls.Status(ctx, b.Descriptor.URLs[0]); err == nil {
			currentUpdateDownloadSize += s.Offset
			b.Fetched = s.Offset
			inFlightBlobs = append(inFlightBlobs, b)
		} else if errors.Is(err, errdefs.ErrNotFound) {
			if i, err := ls.Info(ctx, b.Descriptor.Digest); err == nil {
				currentUpdateDownloadSize += i.Size
				b.Fetched = i.Size
				fetchedBlobs++
			}
		}
	}

	sr.Update(FetchProgress{Blobs: inFlightBlobs, FetchedBlobs: fetchedBlobs, Current: currentUpdateDownloadSize, Total: totalBlobsFetchSize})
}
