package update

import (
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
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

	// blobChecker checks the blobs of the update's apps against the store and sizes the missing ones.
	blobChecker struct {
		u           *runnerImpl
		b           *session
		appStore    compose.AppStore
		ingests     content.Store // the store's ingests: blobs whose fetch was started
		opts        *InitOptions
		progress    InitProgress
		storeSize   int64 // bytes the store still has to receive for the missing blobs, aligned to the block size
		runtimeSize int64 // runtime size of the missing blobs: unpacked layers, extracted bundles
	}
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
	apps, blobCounter, err := u.loadUpdateApps(ctx, srcBlobProvider, &opts)
	if err != nil {
		return err
	}

	appStore, err := v1.NewAppStore(u.config.StoreRoot, u.config.Platform, false)
	if err != nil {
		return err
	}
	ls, err := local.NewStore(u.config.StoreRoot)
	if err != nil {
		return err
	}
	checker := newBlobChecker(u, b, appStore, ls, &opts, blobCounter)
	if opts.ProgressReporter != nil {
		opts.ProgressReporter.Update(checker.progress)
	}

	u.Blobs = make(compose.BlobsFetchProgress)
	for _, app := range apps {
		if err := app.Tree().Walk(func(node *compose.TreeNode, depth int) error {
			return checker.checkBlob(ctx, app, node)
		}); err != nil {
			return err
		}
	}

	needs, err := compose.StorageNeeds(ctx, u.config, srcBlobProvider, apps, checker.storeSize, checker.runtimeSize)
	if err != nil {
		return err
	}
	u.Storage, err = compose.MergeStorageNeeds(needs)
	return err
}

func (u *runnerImpl) loadUpdateApps(ctx context.Context, provider compose.BlobProvider, opts *InitOptions) ([]compose.App, int, error) {
	p := InitProgress{
		State:   UpdateInitStateLoadingTree,
		Current: 0,
		Total:   len(u.URIs),
	}
	if opts.ProgressReporter != nil {
		opts.ProgressReporter.Update(p)
	}
	var apps []compose.App
	var blobCounter int
	for _, appURI := range u.URIs {
		// TODO: add support of progress reporting for loading app trees
		app, err := u.config.AppLoader.LoadAppTree(ctx, provider, platforms.OnlyStrict(u.config.Platform), appURI)
		if err != nil {
			return nil, 0, err
		}
		apps = append(apps, app)
		blobCounter += app.NodeCount()
		if opts.ProgressReporter != nil {
			p.Current += 1
			opts.ProgressReporter.Update(p)
		}
	}
	return apps, blobCounter, nil
}

func newBlobChecker(u *runnerImpl, b *session, appStore compose.AppStore, ingests content.Store,
	opts *InitOptions, blobCount int) *blobChecker {
	return &blobChecker{u: u, b: b, appStore: appStore, ingests: ingests, opts: opts,
		progress: InitProgress{State: UpdateInitStateCheckingBlobs, Total: blobCount}}
}

func (c *blobChecker) checkBlob(ctx context.Context, app compose.App, node *compose.TreeNode) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	checkOpts := []compose.SecureReadOptions{
		compose.WithExpectedSize(node.Descriptor.Size),
		compose.WithExpectedDigest(node.Descriptor.Digest),
		compose.WithRef(node.Ref()),
	}
	bs, err := compose.CheckBlob(compose.WithAppRef(compose.WithBlobType(ctx, node.Type), app.Ref()),
		c.appStore, node.Descriptor.Digest, checkOpts...)
	if err != nil {
		return err
	}
	if bs != compose.BlobOk && c.u.Blobs[node.Descriptor.Digest] == nil {
		c.addMissingBlob(ctx, app, node, bs)
	}
	if c.opts.ProgressReporter != nil {
		c.progress.Current += 1
		c.opts.ProgressReporter.Update(c.progress)
	}
	c.u.Progress = int(float64(c.progress.Current) / float64(c.progress.Total) * 100)
	return c.b.write(&c.u.Update)
}

func (c *blobChecker) addMissingBlob(ctx context.Context, app compose.App, node *compose.TreeNode, bs compose.BlobState) {
	blobStoreSize := compose.AlignToBlockSize(node.Descriptor.Size, c.u.config.BlockSize)
	blobRuntimeSize := app.GetBlobRuntimeSize(node.Descriptor, c.u.config.Platform.Architecture, c.u.config.BlockSize)

	state := bs
	var bytesFetched int64
	if fetchStatus, err := c.ingests.Status(ctx, node.Ref()); err == nil {
		state = compose.BlobFetching
		bytesFetched = fetchStatus.Offset
	}

	c.u.Blobs[node.Descriptor.Digest] = &compose.BlobFetchProgress{
		BlobInfo: compose.BlobInfo{
			Descriptor:   node.Descriptor,
			State:        state,
			Type:         node.Type,
			StoreSize:    blobStoreSize,
			RuntimeSize:  blobRuntimeSize,
			BytesFetched: bytesFetched,
		},
		BytesFetched: bytesFetched,
	}
	// The store still has to receive the bytes that are not ingested yet; the runtime size is the full one.
	c.storeSize += compose.AlignToBlockSize(node.Descriptor.Size-bytesFetched, c.u.config.BlockSize)
	c.runtimeSize += blobRuntimeSize
	c.u.TotalBlobsBytes += node.Descriptor.Size - bytesFetched
}
