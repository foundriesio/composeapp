package compose

import (
	"bytes"
	"context"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"os"
	"os/exec"
)

type (
	StartOptions struct {
		Verbose         bool
		ProgressHandler AppStartProgress
	}

	StartOption func(*StartOptions)

	AppStartStatus   string
	AppStartProgress func(app App, status AppStartStatus, any interface{})
)

const (
	AppStartStatusStarting AppStartStatus = "starting"
	AppStartStatusStarted  AppStartStatus = "started"
	AppStartStatusFailed   AppStartStatus = "failed"
)

func WithVerboseStart(verbose bool) StartOption {
	return func(o *StartOptions) {
		o.Verbose = verbose
	}
}

func WithStartProgressHandler(handler AppStartProgress) StartOption {
	return func(o *StartOptions) {
		o.ProgressHandler = handler
	}
}

func StartApps(ctx context.Context, cfg *Config, appURIs []string, options ...StartOption) error {
	opts := &StartOptions{
		Verbose: false,
	}
	for _, o := range options {
		o(opts)
	}

	cs, err := cfg.AppStoreFactory()
	if err != nil {
		return err
	}

	apps := map[string]App{}
	for _, appURI := range appURIs {
		app, err := cfg.AppLoader.LoadAppTree(ctx, cs, platforms.OnlyStrict(cfg.Platform), appURI)
		if err != nil {
			return err
		}
		apps[appURI] = app
	}

	for _, app := range apps {
		if opts.ProgressHandler != nil {
			opts.ProgressHandler(app, AppStartStatusStarting, nil)
		}
		cmd := exec.Command("docker", "compose", "up", "-d", "--remove-orphans")
		cmd.Dir = cfg.GetAppComposeDir(app.Name())
		if opts.Verbose {
			// Directly connect to stdout/stderr, so we can see the output in real time
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
		} else {
			// Capture stdout/stderr for error reporting
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
		}
		if err := cmd.Run(); err != nil {
			if opts.ProgressHandler != nil {
				opts.ProgressHandler(app, AppStartStatusFailed, err)
			}
			if opts.Verbose {
				return fmt.Errorf("failed to start %s: %w", app, err)
			} else {
				return fmt.Errorf("failed to start %s: %w\n\tstdout: %s\n\tstderr: %s", app, err, cmd.Stdout, cmd.Stderr)
			}

		}
		if opts.ProgressHandler != nil {
			opts.ProgressHandler(app, AppStartStatusStarted, nil)
		}
	}
	return nil
}
