package updatectl

import (
	"github.com/sirupsen/logrus"
	"os"
)

func ExitIfNotNil(err error) {
	if err == nil {
		return
	}
	logrus.Errorf("%v", err)
	os.Exit(-1)
}
