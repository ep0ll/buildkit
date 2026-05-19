package buildhistory

import (
	"context"
	"os"

	"github.com/containerd/containerd/v2/core/leases"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/pkg/errors"
	bolt "go.etcd.io/bbolt"
)

func (q *Queue) Delete(ctx context.Context, ref string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	deleted, err := q.deleteRecord(ref)
	if err != nil {
		return err
	}
	if deleted {
		return q.opt.GarbageCollect(ctx)
	}
	return nil
}

func (q *Queue) deleteRecord(ref string) (bool, error) {
	if q.isRecordInUse(ref) {
		q.pendingDel[ref] = struct{}{}
		return false, nil
	}
	if fn := q.opt.Hooks.BeforeDelete; fn != nil {
		if err := fn(context.Background(), ref); err != nil {
			return false, err
		}
	}
	delete(q.pendingDel, ref)
	q.events.Send(&controlapi.BuildHistoryEvent{
		Type:   controlapi.BuildHistoryEventType_DELETED,
		Record: &controlapi.BuildHistoryRecord{Ref: ref},
	})
	return true, q.removeRecordFromStore(ref)
}

func (q *Queue) isRecordInUse(ref string) bool {
	_, ok := q.refLocks[ref]
	return ok
}

func (q *Queue) removeRecordFromStore(ref string) error {
	return q.opt.DB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(recordsBucket))
		if b == nil {
			return errors.Wrapf(os.ErrNotExist, "failed to retrieve bucket %s", recordsBucket)
		}
		if err := b.Delete([]byte(ref)); err != nil {
			return err
		}
		return q.hLeaseManager.Delete(context.TODO(), leases.Lease{ID: q.leaseID(ref)})
	})
}

func (q *Queue) lockRecord(ref string) {
	q.refLocks[ref]++
}

func (q *Queue) unlockRecord(ref string) {
	q.refLocks[ref]--
	if q.refLocks[ref] > 0 {
		return
	}
	delete(q.refLocks, ref)
	if _, pending := q.pendingDel[ref]; !pending {
		return
	}
	q.deleteRecord(ref)
}
