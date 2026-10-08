package compose

import (
	"bytes"
	"context"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"os"
	"os/exec"
	"strings"
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
		if err := composeUp(cfg.GetAppComposeDir(app.Name()), opts.Verbose); err != nil {
			if opts.ProgressHandler != nil {
				opts.ProgressHandler(app, AppStartStatusFailed, err)
			}
			return fmt.Errorf("failed to start %s: %w", app, err)
		}
		if opts.ProgressHandler != nil {
			opts.ProgressHandler(app, AppStartStatusStarted, nil)
		}
	}
	return nil
}

// composeUp runs `docker compose up` in the given app compose directory.
// If it fails, the app containers that were created but not started are removed.
func composeUp(composeDir string, verbose bool) error {
	cmd := exec.Command("docker", "compose", "up", "-d", "--remove-orphans")
	cmd.Dir = composeDir
	var stdout, stderr bytes.Buffer
	if verbose {
		// Directly connect to stdout/stderr, so we can see the output in real time
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	} else {
		// Capture stdout/stderr for error reporting
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}
	err := cmd.Run()
	if err == nil {
		return nil
	}
	if !verbose {
		err = fmt.Errorf("%w\n\tstdout: %s\n\tstderr: %s", err, stdout.String(), stderr.String())
	}
	if errRm := removeCreatedContainers(composeDir); errRm != nil {
		err = fmt.Errorf("%w\n\tfailed to clean up after the failed start: %s", err, errRm.Error())
	}
	return err
}

// removeCreatedContainers removes the app containers that `docker compose up` created but failed to start.
// Starting such a container again may succeed without its published ports (Docker 29.7.2 does so after
// a host port conflict), so the next start attempt could report success for an app that is broken.
func removeCreatedContainers(composeDir string) error {
	cmd := exec.Command("docker", "compose", "ps", "--all", "--quiet", "--status", "created")
	cmd.Dir = composeDir
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to list containers in the created state: %w", err)
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil
	}
	cmd = exec.Command("docker", append([]string{"rm"}, ids...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to remove containers in the created state: %w: %s", err, out)
	}
	return nil
}
