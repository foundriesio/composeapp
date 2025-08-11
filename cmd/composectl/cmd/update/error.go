package updatectl

import (
	"context"
	"fmt"
	"github.com/pkg/errors"
	"os"
)

func ExitIfNotNil(err error) {
	if err == nil {
		return
	}
	if !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "ERROR: %s\n", err.Error())
	}
	os.Exit(-1)
}
