package docker

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
)

func NewTarStreamer() *tarStreamer {
	r, w := io.Pipe()
	return &tarStreamer{
		tw: tar.NewWriter(w),
		r:  r,
		w:  w,
	}
}

func (ts *tarStreamer) Close() error {
	ts.w.Close()
	return ts.tw.Close()
}

func (ts *tarStreamer) Reader() io.Reader {
	return ts.r
}

func (ts *tarStreamer) WriteFileData(data []byte, dataFileName string) (err error) {
	return ts.write(&tar.Header{
		Typeflag: tar.TypeReg,
		Format:   tar.FormatPAX,
		Name:     dataFileName,
		Size:     int64(len(data)),
	}, bytes.NewReader(data))
}

func (ts *tarStreamer) WriteFiles(files []string) (err error) {
	for _, path := range files {
		if err := ts.writeFile(path); err != nil {
			return err
		}
	}
	return err
}

func (ts *tarStreamer) writeFile(path string) (err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		return err
	}
	// do we really need it?
	hdr.Name = fi.Name()
	hdr.Format = tar.FormatPAX

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	return ts.write(hdr, f)
}

func (ts *tarStreamer) write(h *tar.Header, r io.Reader) (err error) {
	if err := ts.tw.WriteHeader(h); err != nil {
		return err
	}
	n, err := io.Copy(ts.tw, r)
	if err != nil {
		return err
	}
	if n != h.Size {
		return fmt.Errorf("failed to write required number of bytes to tar streamer;"+
			" required: %d, written: %d", h.Size, n)
	}
	return err
}
