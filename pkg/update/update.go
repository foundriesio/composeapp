package update

import (
	"context"
	"errors"
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/google/uuid"
	"time"
)

type (
	Runner interface {
		Init(context.Context, []string, ...InitOption) error
		Fetch(context.Context, ...FetchOption) error
		Install(context.Context, ...compose.InstallOption) error
		Run(context.Context) error
		Cancel(context.Context) error
		Info() Update
	}

	State string

	BlobStatus struct {
		compose.BlobInfo
		Downloaded int64 `json:"downloaded"`
	}

	Update struct {
		ID                    string                 `json:"id"`
		ClientRef             string                 `json:"client_ref"`
		State                 State                  `json:"state"`
		Progress              int                    `json:"progress"`
		Timestamp             time.Time              `json:"timestamp"`
		URIs                  []string               `json:"uris"`
		Blobs                 map[string]*BlobStatus `json:"blobs"`
		TotalBlobDownloadSize int64                  `json:"total_blob_download_size"`
	}

	runnerImpl struct {
		Update
		config *compose.Config
		store  *store
	}
)

const (
	StateCreated      State = "update:state:created"
	StateInitializing State = "update:state:initializing"
	StateInitialized  State = "update:state:initialized"
	StateFetching     State = "update:state:fetching"
	StateFetched      State = "update:state:fetched"
	StateInstalling   State = "update:state:installing"
	StateInstalled    State = "update:state:installed"
	StateRunning      State = "update:state:running"
	StateCompleted    State = "update:state:completed"
	StateFailed       State = "update:state:failed"
)

func NewUpdate(cfg *compose.Config, ref string) (Runner, error) {
	s, err := newStore(cfg.DBFilePath)
	if err != nil {
		return nil, err
	}

	_, err = s.getCurrentUpdate()
	if err == nil {
		return nil, errors.New("update already in progress")
	} else if err != nil && !errors.Is(err, ErrUpdateNotFound) {
		return nil, err
	}

	u := &runnerImpl{
		Update: Update{
			ID:        uuid.New().String(),
			ClientRef: ref,
			State:     StateCreated,
			Progress:  0,
			Timestamp: time.Now(),
		},
		config: cfg,
		store:  s,
	}
	err = s.saveUpdate(&u.Update)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func GetCurrentUpdate(cfg *compose.Config) (Runner, error) {
	s, err := newStore(cfg.DBFilePath)
	if err != nil {
		return nil, err
	}
	u, err := s.getCurrentUpdate()
	if err != nil {
		return nil, err
	}
	return &runnerImpl{
		Update: *u,
		config: cfg,
		store:  s,
	}, nil
}

func GetLastUpdate(cfg *compose.Config) (Runner, error) {
	s, err := newStore(cfg.DBFilePath)
	if err != nil {
		return nil, err
	}
	u, err := s.getLastUpdate()
	if err != nil {
		return nil, err
	}
	return &runnerImpl{
		Update: *u,
		config: cfg,
		store:  s,
	}, nil
}

func (u *runnerImpl) Init(ctx context.Context, appURIs []string, options ...InitOption) error {
	return u.store.update(ctx, func(b *bucket) error {
		return u.initUpdate(ctx, b, appURIs, options...)
	})
}

func (u *runnerImpl) Fetch(ctx context.Context, options ...FetchOption) error {
	return u.store.update(ctx, func(b *bucket) error {
		return u.fetch(ctx, b, options...)
	})
}

func (u *runnerImpl) Install(ctx context.Context, options ...compose.InstallOption) error {
	return u.store.update(ctx, func(b *bucket) error {
		return u.install(ctx, b, options...)
	})
}

func (u *runnerImpl) Run(ctx context.Context) error {
	return u.store.update(ctx, func(b *bucket) error {
		return u.run(ctx, b)
	})
}

func (u *runnerImpl) Info() Update {
	return u.Update
}

func (u *runnerImpl) Cancel(ctx context.Context) error {
	// TODO: Implement removing the update blobs from the store
	return u.store.remove(u.ID)
}

func (s State) String() string {
	return string(s[len("update:state:"):])
}
