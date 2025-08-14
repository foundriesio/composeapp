package update

import (
	"context"
	"errors"
	"fmt"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/google/uuid"
	"time"
)

type (
	Runner interface {
		Status() Update
		Init(context.Context, []string, ...InitOption) error
		Fetch(context.Context, ...compose.FetchOption) error
		Install(context.Context, ...compose.InstallOption) error
		Start(context.Context) error
		Cancel(context.Context) error
		Complete(context.Context, ...CompleteOpt) error
	}

	State string

	Update struct {
		ID              string                     `json:"id"`
		ClientRef       string                     `json:"client_ref"`
		State           State                      `json:"state"`
		Progress        int                        `json:"progress"`
		CreationTime    time.Time                  `json:"timestamp"`
		UpdateTime      time.Time                  `json:"update_time"`
		URIs            []string                   `json:"uris"`
		Blobs           compose.BlobsFetchProgress `json:"blobs"`
		TotalBlobsBytes int64                      `json:"total_blobs_bytes"` // total size of all blobs in bytes
		LoadedImages    map[string]struct{}        `json:"loaded_images"`     // images that have been loaded into the docker storage
		FetchedBytes    int64                      `json:"fetched_bytes"`     // total bytes fetched so far
		FetchedBlobs    int                        `json:"fetched_blobs"`     // number of blobs fetched so far
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
	StateStarting     State = "update:state:starting"
	StateStarted      State = "update:state:started"
	StateCompleting   State = "update:state:completing"
	StateCompleted    State = "update:state:completed"
	StateFailed       State = "update:state:failed"
	StateCancelling   State = "update:state:cancelling"
	StateCanceled     State = "update:state:canceled"
)

func (s State) String() string {
	return string(s[len("update:state:"):])
}

func NewUpdate(cfg *compose.Config, ref string) (Runner, error) {
	_, err := GetCurrentUpdate(cfg)
	if err == nil {
		return nil, errors.New("update already in progress")
	} else if !errors.Is(err, ErrUpdateNotFound) {
		return nil, err
	}

	s, err := newStore(cfg.DBFilePath)
	if err != nil {
		return nil, err
	}

	u := &runnerImpl{
		Update: Update{
			ID:           uuid.New().String(),
			ClientRef:    ref,
			State:        StateCreated,
			Progress:     0,
			CreationTime: time.Now(),
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

func GetFinalizedUpdate(cfg *compose.Config) (*Update, error) {
	s, err := newStore(cfg.DBFilePath)
	if err != nil {
		return nil, err
	}
	return s.getLastUpdateWithAnyOfStates([]State{
		StateCompleted,
		StateFailed,
		StateCanceled,
	})
}

func GetLastSuccessfulUpdate(cfg *compose.Config) (*Update, error) {
	s, err := newStore(cfg.DBFilePath)
	if err != nil {
		return nil, err
	}
	return s.getLastUpdateWithAnyOfStates([]State{
		StateCompleted,
	})
}

func GetCurrentUpdate(cfg *compose.Config) (Runner, error) {
	s, err := newStore(cfg.DBFilePath)
	if err != nil {
		return nil, err
	}
	u, err := s.getLastUpdateWithAnyOfStates([]State{
		StateCreated,
		StateInitializing,
		StateInitialized,
		StateFetching,
		StateFetched,
		StateInstalling,
		StateInstalled,
		StateStarting,
		StateStarted,
		StateCompleting,
		StateCancelling,
	})
	if err != nil {
		return nil, err
	}
	return &runnerImpl{
		Update: *u,
		config: cfg,
		store:  s,
	}, nil
}

func (u *runnerImpl) Status() Update {
	return u.Update
}

func (u *runnerImpl) Init(ctx context.Context, appURIs []string, options ...InitOption) error {
	opts := InitOptions{}
	for _, o := range options {
		o(&opts)
	}
	return u.store.lock(func(db *session) error {
		var err error
		switch u.State {
		case StateCreated:
			{
				if len(appURIs) == 0 && !opts.AllowEmptyAppList {
					return fmt.Errorf("no app URIs for an update are specified")
				}
				u.URIs = appURIs
			}
		case StateInitializing, StateInitialized, StateFetching, StateFetched:
			{
				// reinitialize the current Update
				if len(appURIs) > 0 {
					return fmt.Errorf("cannot reinitialize an existing update with new app URIs specified")
				}
				u.Blobs = nil
				u.TotalBlobsBytes = 0
				u.FetchedBlobs = 0
				u.FetchedBytes = 0
				u.Progress = 0
			}
		default:
			return fmt.Errorf("cannot reinitialize an update when it is in state '%s'", u.State)
		}

		u.State = StateInitializing
		err = db.write(&u.Update)
		if err != nil {
			return err
		}

		defer func() {
			if err == nil {
				u.Progress = 100
				if opts.CheckStatus && len(u.Blobs) == 0 && u.TotalBlobsBytes == 0 {
					if s, err := compose.CheckAppsStatus(ctx, u.config, u.URIs); err == nil {
						if s.AreFetched() {
							u.State = StateFetched
						}
						if s.AreInstalled() {
							u.State = StateInstalled
						}
						if s.AreRunning() {
							u.State = StateStarted
						}
					}
				} else {
					u.State = StateInitialized
				}
			} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				u.State = StateFailed
			}
			if err := db.write(&u.Update); err != nil {
				// log the error but do not return it
				fmt.Printf("failed to write update: %v\n", err)
			}
		}()
		err = u.initUpdate(ctx, db, options...)
		return err
	})
}

func (u *runnerImpl) Fetch(ctx context.Context, options ...compose.FetchOption) error {
	return u.store.lock(func(db *session) error {
		var err error
		switch u.State {
		case StateFetching, StateInitialized:
			// the current state is correct to fetch updates
		default:
			return fmt.Errorf("cannot fetch update when it is in state '%s'", u.State.String())
		}

		u.State = StateFetching
		u.Progress = 0
		err = db.write(&u.Update)
		if err != nil {
			return err
		}

		defer func() {
			if err == nil {
				u.Progress = 100
				u.State = StateFetched
				if appStore, err := v1.NewAppStore(u.config.StoreRoot, u.config.Platform, false); err != nil {
					// log the error but do not return it
					fmt.Printf("failed to create app store: %v\n", err)
				} else {
					if err := appStore.AddApps(u.URIs); err != nil {
						fmt.Printf("failed to add info about fetched apps to the store: %v\n", err)
					}
				}
			} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				u.State = StateFailed
			}
			if err := db.write(&u.Update); err != nil {
				// log the error but do not return it
				fmt.Printf("failed to write update: %v\n", err)
			}
		}()

		err = u.fetch(ctx, db, options...)
		return err
	})
}

func (u *runnerImpl) Install(ctx context.Context, options ...compose.InstallOption) error {
	return u.store.lock(func(db *session) error {
		var err error
		switch u.State {
		case StateFetched, StateInstalling, StateInstalled:
			// the current state is correct to install updates
		default:
			return fmt.Errorf("cannot install update when it is in state '%s'", u.State.String())
		}
		u.State = StateInstalling
		u.Progress = 0
		err = db.write(&u.Update)
		if err != nil {
			return err
		}

		defer func() {
			if err == nil {
				u.State = StateInstalled
			} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				u.State = StateFailed
			}
			if err := db.write(&u.Update); err != nil {
				// log the error but do not return it
				fmt.Printf("failed to write update: %v\n", err)
			}
		}()

		err = u.install(ctx, db, options...)
		return err
	})
}

func (u *runnerImpl) Start(ctx context.Context) error {
	return u.store.lock(func(db *session) error {
		switch u.State {
		case StateInstalled, StateStarting:
			// the current state is correct to start updates
		default:
			return fmt.Errorf("cannot start update when it is in state '%s'", u.State.String())
		}

		u.State = StateStarting
		u.Progress = 0
		err := db.write(&u.Update)
		if err != nil {
			return err
		}

		defer func() {
			if err == nil {
				u.State = StateStarted
			} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				u.State = StateFailed
			}
			if err := db.write(&u.Update); err != nil {
				// log the error but do not return it
				fmt.Printf("failed to write update: %v\n", err)
			}
		}()

		return u.run(ctx, db)
	})
}

func (u *runnerImpl) Cancel(ctx context.Context) error {
	return u.store.lock(func(db *session) error {
		var err error
		switch u.State {
		case StateCreated, StateInitializing, StateInitialized, StateFetching, StateFetched, StateInstalling, StateInstalled:
			// the current state is correct to cancel updates
		default:
			return fmt.Errorf("cannot cancel update when it is in state '%s'", u.State.String())
		}

		u.State = StateCancelling
		u.Progress = 0
		err = db.write(&u.Update)
		if err != nil {
			return err
		}

		defer func() {
			if err == nil {
				u.State = StateCanceled
			} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				u.State = StateFailed
			}
			if err := db.write(&u.Update); err != nil {
				// log the error but do not return it
				fmt.Printf("failed to write update: %v\n", err)
			}
		}()

		err = u.cancel(ctx)
		return err
	})
}

func (u *runnerImpl) Complete(ctx context.Context, options ...CompleteOpt) error {
	return u.store.lock(func(db *session) error {
		var err error
		switch u.State {
		case StateStarted, StateCompleting:
			// the current state is correct to complete updates
		default:
			return fmt.Errorf("cannot complete update when it is in state '%s'", u.State.String())
		}

		u.State = StateCompleting
		u.Progress = 0
		err = db.write(&u.Update)
		if err != nil {
			return err
		}

		defer func() {
			if err == nil {
				u.Progress = 100
				u.State = StateCompleted
			} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, compose.ErrUninstallRunningApps) {
				u.State = StateFailed
			}
			if err := db.write(&u.Update); err != nil {
				// log the error but do not return it
				fmt.Printf("failed to write update: %v\n", err)
			}
		}()

		err = u.complete(ctx, options...)
		return err
	})
}
