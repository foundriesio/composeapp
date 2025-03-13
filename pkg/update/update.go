package update

import (
	"context"
	"errors"
	"fmt"
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/google/uuid"
	"time"
)

type (
	Runner interface {
		Status() Update
		Init(context.Context, []string, ...InitOption) error
		Fetch(context.Context, ...FetchOption) error
		Install(context.Context, ...compose.InstallOption) error
		Start(context.Context) error
		Cancel(context.Context) error
		Complete(context.Context) error
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
		CreationTime          time.Time              `json:"timestamp"`
		UpdateTime            time.Time              `json:"update_time"`
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

func (u *runnerImpl) Status() Update {
	return u.Update
}

func (u *runnerImpl) Init(ctx context.Context, appURIs []string, options ...InitOption) error {
	return u.store.lock(func(db *session) error {
		var err error
		switch u.State {
		case StateCreated:
			{
				if len(appURIs) == 0 {
					return fmt.Errorf("no app URIs for an update are specified")
				}
				u.URIs = appURIs
			}
		case StateInitializing, StateInitialized:
			{
				// reinitialize the current Update
				if len(appURIs) > 0 {
					return fmt.Errorf("cannot reinitialize an existing update with new app URIs specified")
				}
				u.Blobs = nil
				u.TotalBlobDownloadSize = 0
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
				u.State = StateInitialized
			} else if !(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
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

func (u *runnerImpl) Fetch(ctx context.Context, options ...FetchOption) error {
	return u.store.lock(func(db *session) error {
		var err error
		if !(u.State == StateFetching || u.State == StateInitialized) {
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
			} else if !(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
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
		if !(u.State == StateFetched || u.State == StateInstalling || u.State == StateInstalled) {
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
			} else if !(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
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
		if !(u.State == StateInstalled || u.State == StateStarting) {
			return fmt.Errorf("cannot run update when it is in state '%s'", u.State.String())
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
			} else if !(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
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
		if !(u.State == StateCreated ||
			u.State == StateInitializing ||
			u.State == StateInitialized ||
			u.State == StateFetching ||
			u.State == StateFetched ||
			u.State == StateInstalling ||
			u.State == StateInstalled) {
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
			} else if !(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
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

func (u *runnerImpl) Complete(ctx context.Context) error {
	return u.store.lock(func(db *session) error {
		var err error
		if !(u.State == StateStarted || u.State == StateCompleting) {
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
			} else if !(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				u.State = StateFailed
			}
			if err := db.write(&u.Update); err != nil {
				// log the error but do not return it
				fmt.Printf("failed to write update: %v\n", err)
			}
		}()

		err = u.complete(ctx)
		return err
	})
}
