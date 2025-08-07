package update

import (
	"github.com/containerd/containerd/platforms"
	"github.com/foundriesio/composeapp/internal/progress"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"golang.org/x/net/context"
)

type (
	InitState string

	InitProgress struct {
		State   InitState
		Current int
		Total   int
	}

	InitProgressFunc func(initProgress *InitProgress)

	InitOptions struct {
		ProgressReporter  progress.Reporter[InitProgress]
		AllowEmptyAppList bool // Allow specifying an empty app list, which means updating to "no apps" state, hence removing all current apps.
		CheckStatus       bool // Check the status of the specified apps and move the update state to the state that corresponds to this status.
	}

	InitOption func(options *InitOptions)
)

const (
	UpdateInitStateLoadingTree   InitState = "Update:init:loading_tree"
	UpdateInitStateCheckingBlobs InitState = "Update:init:checking_blobs"
)

func WithInitProgress(pf InitProgressFunc) InitOption {
	return func(o *InitOptions) {
		o.ProgressReporter = progress.NewReporter[InitProgress](20)
		o.ProgressReporter.Start(pf)
	}
}

func WithInitAllowEmptyAppList(allowEmptyList bool) InitOption {
	return func(o *InitOptions) {
		o.AllowEmptyAppList = allowEmptyList
	}
}

func WithInitCheckStatus(checkStatus bool) InitOption {
	return func(o *InitOptions) {
		o.CheckStatus = checkStatus
	}
}

func (u *runnerImpl) initUpdate(ctx context.Context, b *session, options ...InitOption) (err error) {
	opts := InitOptions{}
	for _, o := range options {
		o(&opts)
	}

	defer func() {
		if opts.ProgressReporter != nil {
			opts.ProgressReporter.Stop(ctx.Err() == nil)
		}
	}()

	srcBlobProvider := compose.NewRemoteBlobProviderFromConfig(u.config)

	p := InitProgress{
		State:   UpdateInitStateLoadingTree,
		Current: 0,
		Total:   len(u.URIs),
	}
	if opts.ProgressReporter != nil {
		opts.ProgressReporter.Update(p)
	}

	blobCounter := 0

	apps := map[string]compose.App{}
	for _, appURI := range u.URIs {
		// TODO: add support of progress reporting for loading app trees
		app, err := u.config.AppLoader.LoadAppTree(ctx, srcBlobProvider, platforms.OnlyStrict(u.config.Platform), appURI)
		if err != nil {
			return err
		}
		apps[appURI] = app
		blobCounter += app.NodeCount()
		if opts.ProgressReporter != nil {
			p.Current += 1
			opts.ProgressReporter.Update(p)
		}
	}

	appStore, err := v1.NewAppStore(u.config.StoreRoot, u.config.Platform, false)
	if err != nil {
		return err
	}

	var storeSizeTotal int64 = 0
	var runtimeSizeTotal int64 = 0
	var downloadSizeTotal int64 = 0
	var totalSize int64 = 0

	missingBlobs := map[string]*compose.BlobInfo{}
	u.Blobs = missingBlobs

	if opts.ProgressReporter != nil {
		p.State = UpdateInitStateCheckingBlobs
		p.Total = blobCounter
		p.Current = 0
		opts.ProgressReporter.Update(p)
	}

	for appURI, app := range apps {
		err = app.Tree().Walk(func(node *compose.TreeNode, depth int) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			blobURI := node.Descriptor.URLs[0]
			checkOpts := []compose.SecureReadOptions{
				compose.WithExpectedSize(node.Descriptor.Size),
				compose.WithExpectedDigest(node.Descriptor.Digest),
				compose.WithRef(blobURI),
			}
			bs, stateCheckErr := compose.CheckBlob(compose.WithAppRef(compose.WithBlobType(ctx, node.Type),
				apps[appURI].Ref()),
				appStore, node.Descriptor.Digest, checkOpts...)

			if stateCheckErr != nil {
				return stateCheckErr
			}

			if bs != compose.BlobOk && missingBlobs[blobURI] == nil {
				blobStoreSize := compose.AlignToBlockSize(node.Descriptor.Size, u.config.BlockSize)
				blobRuntimeSize := app.GetBlobRuntimeSize(node.Descriptor, u.config.Platform.Architecture, u.config.BlockSize)

				missingBlobs[blobURI] = &compose.BlobInfo{
					Descriptor:   node.Descriptor,
					State:        bs,
					Type:         node.Type,
					StoreSize:    blobStoreSize,
					RuntimeSize:  blobRuntimeSize,
					BytesFetched: 0,
				}
				storeSizeTotal += blobStoreSize
				runtimeSizeTotal += blobStoreSize
				downloadSizeTotal += node.Descriptor.Size
				totalSize += blobStoreSize + blobStoreSize
			}
			if opts.ProgressReporter != nil {
				p.Current += 1
				opts.ProgressReporter.Update(p)
			}

			u.TotalBlobsBytes = downloadSizeTotal
			u.Progress = int(float64(p.Current) / float64(p.Total) * 100)
			if err := b.write(&u.Update); err != nil {
				return err
			}
			return nil
		})

		if err != nil {
			return err
		}
	}

	return nil
}
