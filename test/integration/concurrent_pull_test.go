package e2e_tests

import (
	"strings"
	"testing"

	f "github.com/foundriesio/composeapp/test/fixtures"
)

// TestPullWorkersFlagValidation asserts that out-of-range --workers values are
// rejected with a clear, user-facing error before any pull work begins.
func TestPullWorkersFlagValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"below minimum", "0"},
		{"above maximum", "11"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := f.RunCmdExpectFail(t, "",
				"pull", "registry:5000/factory/does-not-exist:latest", "-w", tc.value)
			msg := string(out)
			if !strings.Contains(msg, "invalid `--workers` value") || !strings.Contains(msg, "between 1 and 10") {
				t.Fatalf("expected an --workers range validation error, got: %s", msg)
			}
		})
	}
}

// TestPullWithConcurrentWorkers verifies the --workers flag plumbs through to the
// concurrent fetch and that an app pulled with multiple workers is fully fetched.
func TestPullWithConcurrentWorkers(t *testing.T) {
	appComposeDef := `
services:
  busybox:
    image: ghcr.io/foundriesio/busybox:1.36
    command: sh -c "while true; do sleep 60; done"
`
	app := f.NewApp(t, appComposeDef)
	app.Publish(t)
	defer app.Remove(t)

	app.PullWithWorkers(t, 5)
	app.CheckFetched(t)
}
