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
	BlobFetchProgress struct {
		BlobInfo
		BytesFetched   int64     `json:"bytes_fetched"`
		FetchStartTime time.Time `json:"fetch_start_time"`
		FetchSpeedAvg  int64     `json:"fetch_speed_avg"` // bytes per second
	}
	FetchProgress struct {
		Blobs        map[digest.Digest]*BlobFetchProgress // per-blob metadata and progress
		FetchedCount int                                  // number of fully fetched blobs
		CurrentBytes int64                                // total bytes downloaded so far
		TotalBytes   int64                                // total bytes expected to download
	}

	FetchOptions struct {
		ProgressHandler      FetchProgressFunc
		ProgressPollInterval int // interval between polling/checking blob download status in milliseconds
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

func FetchBlobs(ctx context.Context, cfg *Config, blobs map[digest.Digest]*BlobInfo, options ...FetchOption) error {
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
	blobsToFetch := map[digest.Digest]*BlobFetchProgress{}
	for d, blob := range blobs {
		totalBlobsFetchSize += blob.Descriptor.Size
		blobsToFetch[d] = &BlobFetchProgress{
			BlobInfo: *blob,
		}
	}

	fetchProgress := FetchProgress{
		Blobs:        blobsToFetch,
		FetchedCount: 0,
		CurrentBytes: 0,
		TotalBytes:   totalBlobsFetchSize,
	}

	authorizer := NewRegistryAuthorizer(cfg.DockerCfg, cfg.ConnectTimeout)
	resolver := NewResolver(authorizer, cfg.ConnectTimeout)
	ls, err := local.NewStore(cfg.StoreRoot)
	if err != nil {
		return err
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
			ticker := time.NewTicker(time.Duration(pollInterval) * time.Millisecond)
			defer ticker.Stop()
		done:
			for {
				select {
				case <-ctx.Done():
					break done
				case <-stopChan:
					break done
				case <-ticker.C:
					checkAndUpdateBlobStatus(ctx, &fetchProgress, ls, progressReporter)
				}
			}
			checkAndUpdateBlobStatus(ctx, &fetchProgress, ls, progressReporter)
		}(stopChan)
	}

	for _, bi := range getOrderedBlobsToFetch(blobsToFetch) {
		bi.FetchStartTime = time.Now()
		if err = CopyBlob(ctx, resolver, bi.Ref(), *bi.Descriptor, ls, true); err != nil {
			err = fmt.Errorf("failed to fetch blob %s: %v", bi.Descriptor.Digest, err)
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
	if err != nil {
		return err
	}
	return ctx.Err()
}

func checkAndUpdateBlobStatus(ctx context.Context, fetchProgress *FetchProgress, ls content.Store, sr progress.Reporter[FetchProgress]) {
	for _, b := range fetchProgress.Blobs {
		if b.State == BlobOk {
			// already fetched
			continue
		}
		if s, err := ls.Status(ctx, b.Ref()); err == nil {
			fetchProgress.CurrentBytes += s.Offset - b.BytesFetched
			b.BytesFetched = s.Offset
			if b.State != BlobFetching {
				b.State = BlobFetching
			} else {
				b.FetchSpeedAvg = int64(float64(b.BytesFetched-b.BlobInfo.BytesFetched) / time.Since(b.FetchStartTime).Seconds())
			}
		} else if errors.Is(err, errdefs.ErrNotFound) {
			if i, err := ls.Info(ctx, b.Descriptor.Digest); err == nil {
				fetchProgress.CurrentBytes += i.Size - b.BytesFetched
				b.BytesFetched = i.Size
				b.State = BlobOk
				fetchProgress.FetchedCount++
				b.FetchSpeedAvg = int64(float64(b.BytesFetched-b.BlobInfo.BytesFetched) / time.Since(b.FetchStartTime).Seconds())
			}
		}
	}
	sr.Update(*fetchProgress)
}

func getOrderedBlobsToFetch(blobs map[digest.Digest]*BlobFetchProgress) (blobsToFetch []*BlobFetchProgress) {
	var resumeMeta, resumeData, startMeta, startData []*BlobFetchProgress

	for _, bi := range blobs {
		isData := bi.Type == BlobTypeImageLayer
		switch {
		case bi.State == BlobFetching && !isData:
			resumeMeta = append(resumeMeta, bi)
		case bi.State == BlobFetching && isData:
			resumeData = append(resumeData, bi)
		case bi.State != BlobFetching && !isData:
			startMeta = append(startMeta, bi)
		default: // bi.State != BlobFetching && isData
			startData = append(startData, bi)
		}
	}

	// Order: resume metadata -> start metadata -> resume data -> start data
	blobsToFetch = append(resumeMeta, startMeta...)
	blobsToFetch = append(blobsToFetch, resumeData...)
	blobsToFetch = append(blobsToFetch, startData...)
	return
}
