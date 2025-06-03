package updatectl

import (
	"fmt"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/foundriesio/composeapp/pkg/update"
	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"
)

type (
	installOptions struct{}

	progressRendererCtx struct {
		bar        *progressbar.ProgressBar
		curImageID string
		curLayerID string
	}
)

func init() {
	installCmd := &cobra.Command{
		Use:   "install",
		Short: "Install the updated apps",
		Long:  `Install the updated apps by extracting the compose project and loading its images into the Docker image storage	`,
	}

	opts := installOptions{}

	installCmd.Run = func(cmd *cobra.Command, args []string) {
		installUpdateCmd(cmd, args, &opts)
	}

	UpdateCmd.AddCommand(installCmd)
}

func installUpdateCmd(cmd *cobra.Command, args []string, opts *installOptions) {
	cfg, err := v1.NewDefaultConfig()
	ExitIfNotNil(err)

	updateCtl, err := update.GetCurrentUpdate(cfg)
	ExitIfNotNil(err)

	err = updateCtl.Install(cmd.Context(), compose.WithInstallProgress(getProgressRenderer()))
	ExitIfNotNil(err)
}

func getProgressRenderer() compose.InstallProgressFunc {
	ctx := &progressRendererCtx{}

	return func(p *compose.InstallProgress) {
		// TODO: handle and render info about the compose.AppInstallStateComposeChecking state
		switch p.AppInstallState {
		case compose.AppInstallStateComposeInstalling:
			{
				fmt.Printf("Installing app %s\n", p.AppID)
			}
		case compose.AppInstallStateImagesLoading:
			{
				renderImageLoadingProgress(ctx, p)
			}
		}
	}
}

func renderImageLoadingProgress(ctx *progressRendererCtx, p *compose.InstallProgress) {
	switch p.ImageLoadState {
	case compose.ImageLoadStateLayerLoading:
		{
			if ctx.curImageID != p.ImageID {
				fmt.Printf("  Loading image %s\n", p.ImageID)
				ctx.curImageID = p.ImageID
				ctx.curLayerID = ""
			}
			if ctx.curLayerID != p.ID {
				ctx.bar = progressbar.DefaultBytes(p.Total)
				ctx.bar.Describe(fmt.Sprintf("    %s", p.ID))
				ctx.curLayerID = p.ID
			}
			if err := ctx.bar.Set64(p.Current); err != nil {
				fmt.Printf("Error setting progress bar: %s\n", err.Error())
			}
		}
	case compose.ImageLoadStateLayerSyncing:
		{
			// TODO: render layer syncing progress
			//fmt.Print(".")
		}
	case compose.ImageLoadStateLayerLoaded:
		{
			//fmt.Println("ok")
			ctx.curLayerID = ""
			ctx.bar.Close()
			ctx.bar = nil
		}
	case compose.ImageLoadStateImageLoaded:
		{
			fmt.Printf("  Image loaded: %s\n", p.ImageID)
		}
	case compose.ImageLoadStateImageExist:
		{
			fmt.Printf("  Already exists: %s\n", p.ImageID)
		}
	default:
		fmt.Printf("  Unknown state %s\n", p.ImageLoadState)
	}
}
