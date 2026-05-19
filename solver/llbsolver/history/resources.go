package history

import (
	"context"

	"github.com/containerd/containerd/v2/core/leases"
	cerrdefs "github.com/containerd/errdefs"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/leaseutil"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

func (q *Queue) addResource(ctx context.Context, l leases.Lease, desc *controlapi.Descriptor, detectSkipLayers bool) error {
	if desc == nil {
		return nil
	}
	if err := q.ensureBlobInHistory(ctx, desc.Digest, detectSkipLayers); err != nil {
		return err
	}
	return q.hLeaseManager.AddResource(ctx, l, leases.Resource{
		ID:   desc.Digest,
		Type: "content",
	})
}

func (q *Queue) ensureBlobInHistory(ctx context.Context, digestStr string, detectSkipLayers bool) error {
	dgst := digest.Digest(digestStr)
	_, err := q.hContentStore.Info(ctx, dgst)
	if err == nil {
		return nil
	}
	if !cerrdefs.IsNotFound(err) {
		return err
	}
	return q.migrateMissingBlob(ctx, digestStr, detectSkipLayers)
}

func (q *Queue) migrateMissingBlob(ctx context.Context, digestStr string, detectSkipLayers bool) error {
	lr, ctx, err := leaseutil.NewLease(ctx, q.hLeaseManager,
		leases.WithID("history_migration_"+identity.NewID()), leaseutil.MakeTemporary)
	if err != nil {
		return err
	}
	defer lr.Discard(ctx)

	ok, err := q.migrateBlobV2(ctx, digestStr, detectSkipLayers)
	if err != nil {
		return err
	}
	if !ok {
		return errors.Errorf("unknown blob %s in history", digestStr)
	}
	return nil
}
