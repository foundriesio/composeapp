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
	"io"
	"sync"
	"time"
)

const (
	DefaultPollInterval = 300 // Default interval between polling/checking blob download status in milliseconds
)

type (
	BlobFetchProgress struct {
		BlobInfo
		BytesFetched   int64         `json:"bytes_fetched"` // overall bytes read for this blob and written to the local storage;
		FetchStartTime time.Time     `json:"fetch_start_time"`
		BytesRead      int64         `json:"bytes_read"`      // total blob bytes read from network during the last fetch attempt
		ReadTime       time.Duration `json:"read_time"`       // aggregate time spent reading this blob data from the network
		ReadSpeedAvg   int64         `json:"read_speed_avg"`  // average read speed in bytes per second
		ReadSpeedCur   int64         `json:"read_speed_curr"` // current read speed in bytes per second
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

	readStat struct {
		bytes int64
		start time.Time
		end   time.Time
	}
	readMonitor struct {
		io.ReadSeekCloser
		ctx      context.Context
		b        *BlobFetchProgress
		stopChan chan struct{}
		wg       sync.WaitGroup
		statChan chan readStat
	}
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
			// Initialize with amount of bytes already fetched and written to local storage
			BytesFetched: blob.BytesFetched,
		}
	}

	fetchProgress := FetchProgress{
		Blobs:        blobsToFetch,
		FetchedCount: 0,
		CurrentBytes: 0,
		TotalBytes:   totalBlobsFetchSize,
	}

	blobProvider := NewRemoteBlobProviderFromConfig(cfg)
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
		err = func() error {
			// Get the reader without digest calculation and verification because the writer/ingester of
			// the local store (`ls`) will do that.
			r, err := blobProvider.GetReadCloser(ctx, WithRef(bi.Ref()), WithDescriptor(*bi.Descriptor))
			if err != nil {
				return fmt.Errorf("failed to initiate request to fetch blob %s: %v", bi.Descriptor.Digest, err)
			}
			defer r.Close()
			blobReader, ok := r.(io.ReadSeekCloser)
			if !ok {
				return fmt.Errorf("blob fetch reader for %s does not implement io.ReadSeekCloser", bi.Ref())
			}
			bi.FetchStartTime = time.Now()
			rm := NewReadMonitor(ctx, blobReader, bi)
			rm.Start()
			defer rm.Stop()
			if err := CopyBlob(ctx, rm, bi.Ref(), *bi.Descriptor, ls, true); err != nil {
				return fmt.Errorf("failed to fetch blob %s: %v", bi.Descriptor.Digest, err)
			}
			return nil
		}()
		if err != nil {
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
			}
		} else if errors.Is(err, errdefs.ErrNotFound) {
			if i, err := ls.Info(ctx, b.Descriptor.Digest); err == nil {
				fetchProgress.CurrentBytes += i.Size - b.BytesFetched
				b.BytesFetched = i.Size
				b.State = BlobOk
				fetchProgress.FetchedCount++
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

func NewReadMonitor(ctx context.Context, r io.ReadSeekCloser, b *BlobFetchProgress) *readMonitor {
	return &readMonitor{
		ReadSeekCloser: r,
		ctx:            ctx,
		b:              b,
		stopChan:       make(chan struct{}),
		statChan:       make(chan readStat, 100),
	}
}

func (r *readMonitor) Read(p []byte) (n int, err error) {
	readStartTime := time.Now()
	n, err = r.ReadSeekCloser.Read(p)
	if n > 0 {
		r.statChan <- readStat{
			bytes: int64(n),
			start: readStartTime,
			end:   time.Now(),
		}
	}
	return
}

func (r *readMonitor) Start() {
	const (
		windowSize     = 5 * time.Second // sliding window duration
		maxSamples     = 200             // maximum number of samples to keep in the window
		tickerInterval = windowSize / 4
	)

	var window []readStat

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(tickerInterval)
		defer ticker.Stop()

		for {
			select {
			case stat := <-r.statChan:
				r.b.BytesRead += stat.bytes
				r.b.ReadTime += stat.end.Sub(stat.start)
				window = append(window, stat)

			case <-ticker.C:
				now := time.Now()
				// Drop old samples outside the window
				cutoff := now.Add(-windowSize)
				i := 0
				for ; i < len(window); i++ {
					if window[i].end.After(cutoff) {
						break
					}
				}
				window = window[i:]

				// Enforce max number of samples constraint
				if len(window) > maxSamples {
					window = window[len(window)-maxSamples:]
				}

				// Optionally reset the backing array if it has grown too much, 2x the size of the window
				if cap(window) > 2*len(window) {
					newWindow := make([]readStat, len(window))
					copy(newWindow, window)
					window = newWindow
				}

				// Calculate speed over the window
				var (
					bytesInWindow int64
					timeInWindow  time.Duration
				)
				for _, s := range window {
					bytesInWindow += s.bytes
					timeInWindow += s.end.Sub(s.start)
				}

				r.b.ReadSpeedCur = 0
				if timeInWindow > 0 {
					r.b.ReadSpeedCur = int64(float64(bytesInWindow) / timeInWindow.Seconds())
				}

				if r.b.ReadTime > 0 && r.b.BytesRead > 0 {
					r.b.ReadSpeedAvg = int64(float64(r.b.BytesRead) / r.b.ReadTime.Seconds())
				}

			case <-r.stopChan:
				return
			case <-r.ctx.Done():
				return
			}
		}
	}()
}

func (r *readMonitor) Stop() {
	select {
	case r.stopChan <- struct{}{}:
	default:
	}
	r.wg.Wait()
}
