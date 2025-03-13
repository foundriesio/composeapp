package update

import (
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/foundriesio/composeapp/internal/progress"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"golang.org/x/net/context"
	"time"
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
		ProgressReporter progress.Reporter[InitProgress]
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

func (u *runnerImpl) initUpdate(ctx context.Context, b *bucket, appURIs []string, options ...InitOption) (err error) {
	opts := InitOptions{}
	for _, o := range options {
		o(&opts)
	}

	switch u.State {
	case StateCreated:
		{
			if len(appURIs) == 0 {
				return fmt.Errorf("no app URIs for an update are specified")
			}
			u.State = StateInitializing
			u.URIs = appURIs
		}
	case StateInitializing, StateInitialized:
		{
			// reinitialize the current Update
			if len(appURIs) > 0 {
				return fmt.Errorf("cannot reinitialize an existing update with new app URIs specified")
			}
			u.State = StateInitializing
			u.Progress = 0
			u.Timestamp = time.Now()
			u.Blobs = nil
			u.TotalBlobDownloadSize = 0
		}
	default:
		return fmt.Errorf("cannot reinitialize an update when it is in state '%s'", u.State)
	}

	defer func() {
		if err := b.write(&u.Update); err != nil {
			// log the error but do not return it
			fmt.Printf("failed to write update: %v\n", err)
		}
		if opts.ProgressReporter != nil {
			opts.ProgressReporter.Stop(ctx.Err() == nil)
		}
	}()

	authorizer := compose.NewRegistryAuthorizer(u.config.DockerCfg)
	resolver := compose.NewResolver(authorizer, u.config.ConnectTime)
	srcBlobProvider := compose.NewRemoteBlobProvider(resolver)

	appLoader := v1.NewAppLoader()

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
		app, err := appLoader.LoadAppTree(ctx, srcBlobProvider, platforms.OnlyStrict(u.config.Platform), appURI)
		if err != nil {
			return err
		}
		apps[appURI] = app
		blobCounter += app.Tree().NodeCount()
		if opts.ProgressReporter != nil {
			p.Current += 1
			opts.ProgressReporter.Update(p)
		}
	}

	if err := b.write(&u.Update); err != nil {
		return err
	}

	appStore, err := v1.NewAppStore(u.config.StoreRoot, u.config.Platform, false)
	if err != nil {
		return err
	}

	var storeSizeTotal int64 = 0
	var runtimeSizeTotal int64 = 0
	var downloadSizeTotal int64 = 0
	var totalSize int64 = 0

	missingBlobs := map[string]*BlobStatus{}

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

				missingBlobs[blobURI] = &BlobStatus{
					BlobInfo: compose.BlobInfo{
						Descriptor:  node.Descriptor,
						State:       bs,
						Type:        node.Type,
						StoreSize:   blobStoreSize,
						RuntimeSize: blobRuntimeSize,
					},
					Downloaded: 0,
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

	u.Blobs = missingBlobs
	u.TotalBlobDownloadSize = downloadSizeTotal
	u.Progress = 100
	u.State = StateInitialized

	return nil
}
