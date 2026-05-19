package buildhistory

import (
	"context"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/leases"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/buildkit/util/leaseutil"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/util/grpcerrors"
)

type blobWriter struct {
	mediaType string
	w         content.Writer
	leases    leases.Manager
	lease     leases.Lease
	digester  digest.Digester
	size      int
}

// OpenBlobWriter creates a temporary content blob for history attachments.
func (q *Queue) OpenBlobWriter(ctx context.Context, mediaType string) (*blobWriter, error) {
	l, err := q.hLeaseManager.Create(ctx, leases.WithRandomID(),
		leases.WithExpiration(5*time.Minute), leaseutil.MakeTemporary)
	if err != nil {
		return nil, err
	}

	ctx = leases.WithLease(ctx, l.ID)
	w, err := content.OpenWriter(ctx, q.hContentStore, content.WithRef("history-"+q.leaseID(l.ID)))
	if err != nil {
		q.hLeaseManager.Delete(context.WithoutCancel(ctx), l)
		return nil, err
	}

	return &blobWriter{
		mediaType: mediaType,
		leases:    q.hLeaseManager,
		lease:     l,
		w:         w,
		digester:  digest.Canonical.Digester(),
	}, nil
}

func (w *blobWriter) Write(p []byte) (int, error) {
	if _, err := w.digester.Hash().Write(p); err != nil {
		return 0, err
	}
	n, err := w.w.Write(p)
	w.size += n
	return n, err
}

func (w *blobWriter) Discard() {
	w.w.Close()
	w.leases.Delete(context.TODO(), w.lease)
}

func (w *blobWriter) Commit(ctx context.Context) (*ocispecs.Descriptor, func(), error) {
	dgst := w.digester.Digest()
	sz := int64(w.size)

	if err := w.w.Commit(leases.WithLease(ctx, w.lease.ID), sz, dgst); err != nil {
		if !cerrdefs.IsAlreadyExists(err) {
			w.Discard()
			return nil, nil, err
		}
	}

	desc := &ocispecs.Descriptor{
		MediaType: w.mediaType,
		Digest:    dgst,
		Size:      sz,
	}
	release := func() { w.leases.Delete(context.TODO(), w.lease) }
	return desc, release, nil
}

// ImportError stores a gRPC status blob for a failed build.
func (q *Queue) ImportError(ctx context.Context, err error) (*spb.Status, *controlapi.Descriptor, func(), error) {
	st, ok := grpcerrors.AsGRPCStatus(grpcerrors.ToGRPC(ctx, err))
	if !ok {
		st = status.New(codes.Unknown, err.Error())
	}

	stpb := st.Proto()
	dt, err := proto.Marshal(stpb)
	if err != nil {
		return nil, nil, nil, err
	}

	w, err := q.OpenBlobWriter(ctx, "application/vnd.googeapis.google.rpc.status+proto")
	if err != nil {
		return nil, nil, nil, err
	}

	if _, err := w.Write(dt); err != nil {
		w.Discard()
		return nil, nil, nil, err
	}

	desc, release, err := w.Commit(ctx)
	if err != nil {
		return nil, nil, nil, err
	}

	stpb.Details = nil
	return stpb, descriptorFromOCI(desc), release, nil
}

func descriptorFromOCI(desc *ocispecs.Descriptor) *controlapi.Descriptor {
	return &controlapi.Descriptor{
		Digest:    string(desc.Digest),
		Size:      desc.Size,
		MediaType: desc.MediaType,
	}
}
