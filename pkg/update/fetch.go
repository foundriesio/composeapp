package update

import (
	"context"
	"errors"
	"fmt"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/errdefs"
	"github.com/foundriesio/composeapp/internal/progress"
	"github.com/foundriesio/composeapp/pkg/compose"
	"sync"
	"time"
)

const (
	DefaultPollInterval = 300 // Default interval between polling/checking blob download status in milliseconds
)

type (
	FetchProgress struct {
		Current int64
		Total   int64
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

func (u *runnerImpl) fetch(
	ctx context.Context,
	b *session,
	options ...FetchOption) (err error) {

	opts := FetchOptions{}
	for _, o := range options {
		o(&opts)
	}

	defer func() {
		if opts.ProgressReporter != nil {
			opts.ProgressReporter.Stop(ctx.Err() == nil)
		}
	}()

	authorizer := compose.NewRegistryAuthorizer(u.config.DockerCfg, u.config.ConnectTimeout)
	resolver := compose.NewResolver(authorizer, u.config.ConnectTimeout)

	ls, err := local.NewStore(u.config.StoreRoot)
	if err != nil {
		return err
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
					checkAndUpdateBlobStatus(ctx, b, u, ls, opts.ProgressReporter)
					return
				case <-stopChan:
					checkAndUpdateBlobStatus(ctx, b, u, ls, opts.ProgressReporter)
					return
				default:
					checkAndUpdateBlobStatus(ctx, b, u, ls, opts.ProgressReporter)
				}
				time.Sleep(time.Duration(pollInterval) * time.Millisecond)
			}
		}(stopChan)
	}

	for _, bi := range u.Blobs {
		err = compose.CopyBlob(ctx, resolver, bi.Descriptor.URLs[0], *bi.Descriptor, ls, true)
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

func checkAndUpdateBlobStatus(ctx context.Context, b *session, u *runnerImpl, ls content.Store, sr progress.Reporter[FetchProgress]) {
	var currentUpdateDownloadSize int64
	for ref, b := range u.Blobs {
		if s, err := ls.Status(ctx, ref); err == nil {
			currentUpdateDownloadSize += s.Offset
			b.Downloaded = s.Offset
		} else if errors.Is(err, errdefs.ErrNotFound) {
			if i, err := ls.Info(ctx, b.Descriptor.Digest); err == nil {
				currentUpdateDownloadSize += i.Size
				b.Downloaded = i.Size
			}
		}
	}
	if u.TotalBlobDownloadSize != 0 {
		u.Progress = int((currentUpdateDownloadSize * 100) / u.TotalBlobDownloadSize)
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
	sr.Update(FetchProgress{Current: currentUpdateDownloadSize, Total: u.TotalBlobDownloadSize})
}
