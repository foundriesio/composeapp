package composectl

import (
	"fmt"
	"os"
)

type LastWill = func()

var onLastWill []LastWill

func AddLastWill(lastWill LastWill) {
	onLastWill = append(onLastWill, lastWill)
}

func DieNotNil(err error, message ...string) {
	DieNotNilWithCode(err, 1, message...)
}

func DieNotNilWithCode(err error, exitCode int, message ...string) {
	if err != nil {
		parts := []interface{}{"ERROR:"}
		for _, p := range message {
			parts = append(parts, p)
		}
		parts = append(parts, err)
		fmt.Println(parts...)
		for _, w := range onLastWill {
			w()
		}
		os.Exit(exitCode)
	}
}
