package history

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client"
	containerdsnapshot "github.com/moby/buildkit/snapshot/containerd"
	"github.com/moby/buildkit/util/iohelper"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

const statusFrameHeaderSize = 4

func writeStatusFrame(w *bufio.Writer, buf []byte, msg *controlapi.StatusResponse) ([]byte, error) {
	sz := msg.SizeVT()
	if cap(buf) < sz {
		buf = make([]byte, sz)
	}
	n, err := msg.MarshalToVT(buf[:sz])
	if err != nil {
		return buf, err
	}
	hdr := make([]byte, statusFrameHeaderSize)
	binary.LittleEndian.PutUint32(hdr, uint32(n))
	if _, err := w.Write(hdr); err != nil {
		return buf, err
	}
	if _, err := w.Write(buf[:n]); err != nil {
		return buf, err
	}
	return buf, nil
}

func replayStatusBlob(ctx context.Context, store *containerdsnapshot.Store, logs *controlapi.Descriptor, out chan<- *client.SolveStatus) error {
	if logs == nil {
		return nil
	}
	ra, err := store.ReaderAt(ctx, ocispecs.Descriptor{
		Digest:    digest.Digest(logs.Digest),
		Size:      logs.Size,
		MediaType: logs.MediaType,
	})
	if err != nil {
		return err
	}
	rc := iohelper.ReadCloser(ra)
	defer rc.Close()
	return decodeStatusFrames(bufio.NewReader(rc), out)
}

func decodeStatusFrames(r *bufio.Reader, out chan<- *client.SolveStatus) error {
	buf := make([]byte, 32*1024)
	for {
		frame, err := readStatusFrame(r, buf)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		var sr controlapi.StatusResponse
		if err := sr.UnmarshalVT(frame); err != nil {
			return err
		}
		out <- client.NewSolveStatus(&sr)
	}
}

func readStatusFrame(r *bufio.Reader, buf []byte) ([]byte, error) {
	if _, err := io.ReadAtLeast(r, buf[:statusFrameHeaderSize], statusFrameHeaderSize); err != nil {
		return nil, err
	}
	sz := binary.LittleEndian.Uint32(buf[:statusFrameHeaderSize])
	if sz > uint32(len(buf)) {
		buf = make([]byte, sz)
	}
	if _, err := io.ReadAtLeast(r, buf[:sz], int(sz)); err != nil {
		return nil, err
	}
	return buf[:sz], nil
}
