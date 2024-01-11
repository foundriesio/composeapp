package compose

import (
	"fmt"
	"github.com/docker/go-units"
	"syscall"
)

type (
	StatFS struct {
		BlockSize int64
		Blocks    uint64
		Bfree     uint64
	}

	UsageInfo struct {
		Path       string  `json:"path"`
		SizeB      uint64  `json:"size_b"`
		Free       uint64  `json:"free"`
		FreeP      float32 `json:"free_p"`
		Reserved   uint64  `json:"reserved"`
		ReservedP  float32 `json:"reserved_p"`
		Available  uint64  `json:"available"`
		AvailableP float32 `json:"available_p"`
		Required   uint64  `json:"required"`
		RequiredP  float32 `json:"required_p"`
	}
)

func (u *UsageInfo) Print() {
	fmt.Printf("required: %s (%.2f%%), available: %s (%.2f%%) at %s, size: %s (100%%), free: %s (%.2f%%),"+
		" reserved: %s (%.2f%%)\n",
		units.BytesSize(float64(u.Required)), u.RequiredP,
		units.BytesSize(float64(u.Available)), u.AvailableP,
		u.Path, units.BytesSize(float64(u.SizeB)),
		units.BytesSize(float64(u.Free)), u.FreeP,
		units.BytesSize(float64(u.Reserved)), u.ReservedP)
}

func GetUsageInfo(path string, required int64, watermark uint) (*UsageInfo, error) {
	fsStat, err := GetFsStat(path)
	if err != nil {
		return nil, err
	}
	ui := UsageInfo{
		Path:      path,
		SizeB:     uint64(fsStat.BlockSize) * fsStat.Blocks,
		Free:      fsStat.Bfree * uint64(fsStat.BlockSize),
		FreeP:     (float32(fsStat.Bfree) / float32(fsStat.Blocks)) * 100.0,
		ReservedP: float32(100 - watermark),
		Required:  uint64(required),
	}
	ui.Reserved = uint64((float64(100-watermark) / 100.0) * float64(ui.SizeB))
	ui.RequiredP = (float32(ui.Required) / float32(ui.SizeB)) * 100.0
	if ui.Free > ui.Reserved {
		ui.Available = ui.Free - ui.Reserved
		ui.AvailableP = ui.FreeP - ui.ReservedP
	} else {
		ui.Available = 0
		ui.AvailableP = 0
	}
	return &ui, nil
}

func GetFsStat(path string) (StatFS, error) {
	var statfs syscall.Statfs_t
	if err := syscall.Statfs(path, &statfs); err == nil {
		return StatFS{BlockSize: int64(statfs.Bsize), Blocks: statfs.Blocks, Bfree: statfs.Bfree}, nil
	} else {
		return StatFS{}, err
	}
}

func AlignToBlockSize(value int64, blockSize int64) (aligned int64) {
	r := value % blockSize
	if r > 0 {
		aligned = value + (blockSize - r)
	} else {
		aligned = value
	}
	return
}
