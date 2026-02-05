package main

import (
	"os"

	composectl "github.com/foundriesio/composeapp/cmd/composectl/cmd"
	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

func main() {
	if len(os.Args) != 2 {
		cobra.CheckErr("usage: genman <outdir>")
	}

	outDir := os.Args[1]

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		cobra.CheckErr(err)
	}

	root := composectl.GetRootCmd()

	header := &doc.GenManHeader{
		Title:   "COMPOSECTL",
		Section: "1",
	}

	if err := doc.GenManTree(root, header, outDir); err != nil {
		cobra.CheckErr(err)
	}
}
