package buildhistory

import (
	"context"
	"slices"
	"time"

	"github.com/containerd/containerd/v2/core/leases"
	cerrdefs "github.com/containerd/errdefs"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/util/bklog"
	"github.com/pkg/errors"
	bolt "go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"
)

func (q *Queue) gc() error {
	records, err := q.listUnpinnedRecords()
	if err != nil || len(records) < int(q.opt.CleanConfig.MaxEntries) {
		return err
	}
	return q.deleteRecordsPastRetention(records)
}

func (q *Queue) listUnpinnedRecords() ([]*controlapi.BuildHistoryRecord, error) {
	var records []*controlapi.BuildHistoryRecord
	err := q.opt.DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(recordsBucket))
		if b == nil {
			return nil
		}
		return b.ForEach(func(key, dt []byte) error {
			var rec controlapi.BuildHistoryRecord
			if err := rec.UnmarshalVT(dt); err != nil {
				return errors.Wrapf(err, "failed to unmarshal build record %s", key)
			}
			if rec.Pinned {
				return nil
			}
			records = append(records, &rec)
			return nil
		})
	})
	return records, err
}

func (q *Queue) deleteRecordsPastRetention(records []*controlapi.BuildHistoryRecord) error {
	slices.SortFunc(records, func(a, b *controlapi.BuildHistoryRecord) int {
		return -a.CompletedAt.AsTime().Compare(b.CompletedAt.AsTime())
	})

	q.mu.Lock()
	defer q.mu.Unlock()

	cutoff := time.Now().Add(-q.opt.CleanConfig.MaxAge.Duration)
	for _, rec := range records[q.opt.CleanConfig.MaxEntries:] {
		if !cutoff.After(rec.CompletedAt.AsTime()) {
			continue
		}
		if _, err := q.deleteRecord(rec.Ref); err != nil {
			return err
		}
	}
	return nil
}

func (q *Queue) clearOrphans() error {
	orphans, err := q.findOrphanedRecords()
	if err != nil || len(orphans) == 0 {
		return err
	}
	return q.deleteOrphanedRecords(orphans)
}

func (q *Queue) findOrphanedRecords() ([]*controlapi.BuildHistoryRecord, error) {
	ctx := context.Background()
	var orphans []*controlapi.BuildHistoryRecord
	err := q.opt.DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(recordsBucket))
		if b == nil {
			return nil
		}
		return b.ForEach(func(key, dt []byte) error {
			var rec controlapi.BuildHistoryRecord
			if err := proto.Unmarshal(dt, &rec); err != nil {
				return errors.Wrapf(err, "failed to unmarshal build record %s", key)
			}
			if q.recordHasLeaseResources(ctx, string(key)) {
				return nil
			}
			orphans = append(orphans, &rec)
			return nil
		})
	})
	return orphans, err
}

func (q *Queue) recordHasLeaseResources(ctx context.Context, ref string) bool {
	recs, err := q.hLeaseManager.ListResources(ctx, leases.Lease{ID: q.leaseID(ref)})
	if err != nil && cerrdefs.IsNotFound(err) {
		return false
	}
	return len(recs) > 0
}

func (q *Queue) deleteOrphanedRecords(records []*controlapi.BuildHistoryRecord) error {
	ctx := context.Background()
	q.mu.Lock()
	defer q.mu.Unlock()

	for _, rec := range records {
		bklog.G(ctx).Warnf("deleting build record %s due to missing blobs", rec.Ref)
		if _, err := q.deleteRecord(rec.Ref); err != nil {
			return err
		}
	}
	return nil
}
