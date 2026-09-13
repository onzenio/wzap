package whatsmeow

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"testing"

	"go.mau.fi/whatsmeow"
)

// fakeDownloader feeds canned bytes into the bounded file in small chunks,
// standing in for the whatsmeow client DownloadToFile stream.
type fakeDownloader struct {
	data []byte
	err  error
	file whatsmeow.File
}

func (f *fakeDownloader) DownloadToFile(_ context.Context, _ whatsmeow.DownloadableMessage, file whatsmeow.File) error {
	f.file = file
	if f.err != nil {
		return f.err
	}
	for i := 0; i < len(f.data); i += 4 {
		end := min(i+4, len(f.data))
		if _, err := file.Write(f.data[i:end]); err != nil {
			return err
		}
	}
	return nil
}

func TestDownloadLimitedReturnsBufferedBytes(t *testing.T) {
	download := &fakeDownloader{data: []byte("12345678")}

	data, err := downloadLimited(context.Background(), download, nil, 8)
	if err != nil {
		t.Fatalf("downloadLimited: %v", err)
	}
	if !bytes.Equal(data, download.data) {
		t.Fatalf("data = %q, want %q", data, download.data)
	}
}

func TestDownloadLimitedRejectsStreamOverCap(t *testing.T) {
	download := &fakeDownloader{data: []byte("1234567890")}

	_, err := downloadLimited(context.Background(), download, nil, 8)
	if !errors.Is(err, errMediaTooLarge) {
		t.Fatalf("error = %v, want errMediaTooLarge", err)
	}
	if download.file == nil {
		t.Fatal("downloader never received a file")
	}
	buffered, ok := download.file.(*boundedFile)
	if !ok {
		t.Fatalf("downloader received %T, want *boundedFile", download.file)
	}
	if len(buffered.data) > 9 {
		t.Fatalf("buffered %d bytes, want at most the cap of 9", len(buffered.data))
	}
	if cap(buffered.data) > 9 {
		t.Fatalf("buffer capacity = %d, want it capped at 9", cap(buffered.data))
	}
}

func TestDownloadLimitedPropagatesDownloadError(t *testing.T) {
	wantErr := errors.New("network down")
	download := &fakeDownloader{err: wantErr}

	if _, err := downloadLimited(context.Background(), download, nil, 8); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestBoundedFileAllowsExactlyLimit(t *testing.T) {
	file := newBoundedFile(8)

	if _, err := file.Write([]byte("12345678")); err != nil {
		t.Fatalf("write at the limit: %v", err)
	}
	if got := string(file.Bytes()); got != "12345678" {
		t.Fatalf("bytes = %q, want %q", got, "12345678")
	}
}

func TestBoundedFileRejectsWriteBeyondLimit(t *testing.T) {
	file := newBoundedFile(8)
	if _, err := file.Write([]byte("12345678")); err != nil {
		t.Fatalf("write at the limit: %v", err)
	}

	n, err := file.Write([]byte("9"))
	if !errors.Is(err, errMediaTooLarge) {
		t.Fatalf("error = %v, want errMediaTooLarge", err)
	}
	if n != 0 {
		t.Fatalf("write count = %d, want 0", n)
	}
	if got := len(file.Bytes()); got != 8 {
		t.Fatalf("buffered %d bytes, want the 8 already accepted", got)
	}
}

func TestBoundedFileSeekReadAtTruncate(t *testing.T) {
	file := newBoundedFile(16)
	if _, err := file.Write([]byte("0123456789abcdef")); err != nil {
		t.Fatalf("write: %v", err)
	}

	mac := make([]byte, 4)
	if _, err := file.ReadAt(mac, 12); err != nil {
		t.Fatalf("read at: %v", err)
	}
	if string(mac) != "cdef" {
		t.Fatalf("mac = %q, want %q", mac, "cdef")
	}

	if err := file.Truncate(12); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	rest, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	if string(rest) != "0123456789ab" {
		t.Fatalf("content = %q, want %q", rest, "0123456789ab")
	}
}

func TestMediaLengthClampsOverflow(t *testing.T) {
	if got := mediaLength(math.MaxUint64); got != math.MaxInt64 {
		t.Fatalf("mediaLength(MaxUint64) = %d, want %d", got, int64(math.MaxInt64))
	}
	if got := mediaLength(5); got != 5 {
		t.Fatalf("mediaLength(5) = %d, want 5", got)
	}
}
