package whatsmeow

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"time"

	"go.mau.fi/whatsmeow"
)

// errMediaTooLarge reports a download aborted because the incoming stream grew
// past the configured cap.
var errMediaTooLarge = errors.New("media download exceeds the size limit")

// mediaDownloader is the whatsmeow client facet downloadLimited needs, kept as
// an interface so tests can drive the bounded file without a network download.
type mediaDownloader interface {
	DownloadToFile(ctx context.Context, msg whatsmeow.DownloadableMessage, file whatsmeow.File) error
}

// downloadLimited downloads msg into memory, refusing to buffer more than
// maxBytes+1 so a hostile or misreported attachment cannot exhaust memory or
// bandwidth. The extra byte tells the consumer at the limit from over it.
func downloadLimited(ctx context.Context, downloader mediaDownloader, msg whatsmeow.DownloadableMessage, maxBytes int64) ([]byte, error) {
	limit := maxBytes + 1
	if maxBytes == math.MaxInt64 {
		limit = maxBytes
	}
	file := newBoundedFile(limit)
	if err := downloader.DownloadToFile(ctx, msg, file); err != nil {
		return nil, err
	}
	return file.Bytes(), nil
}

var _ whatsmeow.File = (*boundedFile)(nil)

// boundedFile is an in-memory whatsmeow.File that caps both its content and
// its capacity at limit bytes, implementing the seek/read-at/truncate surface
// the library's download and decrypt pipeline relies on.
type boundedFile struct {
	data  []byte
	off   int64
	limit int64
}

// newBoundedFile returns an empty file that never buffers more than limit
// bytes.
func newBoundedFile(limit int64) *boundedFile {
	return &boundedFile{limit: limit}
}

// Bytes returns the content buffered so far.
func (f *boundedFile) Bytes() []byte {
	return f.data
}

// Read reads from the current position.
func (f *boundedFile) Read(p []byte) (int, error) {
	if f.off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[f.off:])
	f.off += int64(n)
	return n, nil
}

// Write writes at the current position, failing once the content would pass
// the limit.
func (f *boundedFile) Write(p []byte) (int, error) {
	if f.off < 0 {
		return 0, errors.New("negative offset")
	}
	end := f.off + int64(len(p))
	if end > f.limit {
		return 0, errMediaTooLarge
	}
	f.grow(end)
	copy(f.data[f.off:end], p)
	f.off = end
	return len(p), nil
}

// ReadAt reads at off without moving the current position.
func (f *boundedFile) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	if off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// WriteAt writes at off without moving the current position.
func (f *boundedFile) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	end := off + int64(len(p))
	if end > f.limit {
		return 0, errMediaTooLarge
	}
	f.grow(end)
	copy(f.data[off:end], p)
	return len(p), nil
}

// Seek moves the current position.
func (f *boundedFile) Seek(offset int64, whence int) (int64, error) {
	var position int64
	switch whence {
	case io.SeekStart:
		position = offset
	case io.SeekCurrent:
		position = f.off + offset
	case io.SeekEnd:
		position = int64(len(f.data)) + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if position < 0 {
		return 0, errors.New("negative position")
	}
	f.off = position
	return position, nil
}

// Truncate resizes the content to size, never past the limit.
func (f *boundedFile) Truncate(size int64) error {
	if size < 0 {
		return errors.New("negative size")
	}
	if size > f.limit {
		return errMediaTooLarge
	}
	if size > int64(len(f.data)) {
		f.grow(size)
	} else {
		f.data = f.data[:size]
	}
	if f.off > size {
		f.off = size
	}
	return nil
}

// Stat reports the current content size; the pipeline only reads Size.
func (f *boundedFile) Stat() (os.FileInfo, error) {
	return boundedFileInfo{size: int64(len(f.data))}, nil
}

// grow extends the content to size without letting the capacity pass the
// limit, so memory stays bounded even under repeated appends.
func (f *boundedFile) grow(size int64) {
	if int64(len(f.data)) >= size {
		return
	}
	if int64(cap(f.data)) >= size {
		f.data = f.data[:size]
		return
	}
	capacity := int64(cap(f.data)) * 2
	if capacity < size {
		capacity = size
	}
	if capacity > f.limit {
		capacity = f.limit
	}
	grown := make([]byte, size, capacity)
	copy(grown, f.data)
	f.data = grown
}

// boundedFileInfo is the os.FileInfo of a boundedFile.
type boundedFileInfo struct {
	size int64
}

func (i boundedFileInfo) Name() string       { return "media" }
func (i boundedFileInfo) Size() int64        { return i.size }
func (i boundedFileInfo) Mode() os.FileMode  { return 0 }
func (i boundedFileInfo) ModTime() time.Time { return time.Time{} }
func (i boundedFileInfo) IsDir() bool        { return false }
func (i boundedFileInfo) Sys() any           { return nil }
