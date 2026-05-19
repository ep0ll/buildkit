package history

import (
	"strconv"
	"time"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/pkg/errors"
	bolt "go.etcd.io/bbolt"
)

// NewQueue opens the history store and starts background maintenance.
func NewQueue(opt QueueOpt) (*Queue, error) {
	opt = normalizeQueueOpt(opt)

	q, err := newQueue(opt)
	if err != nil {
		return nil, err
	}
	if err := q.runMigrationIfNeeded(); err != nil {
		return nil, err
	}
	q.startBackgroundWorkers()
	return q, nil
}

func normalizeQueueOpt(opt QueueOpt) QueueOpt {
	if opt.CleanConfig == nil {
		opt.CleanConfig = defaultCleanConfig()
	}
	return opt
}

func newQueue(opt QueueOpt) (*Queue, error) {
	q := &Queue{
		opt:        opt,
		events:     newPubsub[*controlapi.BuildHistoryEvent](),
		active:     map[string]*controlapi.BuildHistoryRecord{},
		finalizers: map[string]*recordFinalizer{},
		refLocks:   map[string]int{},
		pendingDel: map[string]struct{}{},
		liveStatus: map[string]*statusPipeline{},
	}
	if err := q.bindHistoryNamespaces(); err != nil {
		return nil, err
	}
	return q, nil
}

func (q *Queue) bindHistoryNamespaces() error {
	ns := q.opt.ContentStore.Namespace()
	ns2 := q.opt.LeaseManager.Namespace()
	if ns != ns2 {
		return errors.Errorf("invalid configuration: content store namespace %q does not match lease manager namespace %q", ns, ns2)
	}
	suffix := ns + "_history"
	q.hContentStore = q.opt.ContentStore.WithNamespace(suffix)
	q.hLeaseManager = q.opt.LeaseManager.WithNamespace(suffix)
	return nil
}

func (q *Queue) runMigrationIfNeeded() error {
	needs, err := q.needsV2Migration()
	if err != nil {
		return err
	}
	if !needs {
		return nil
	}
	return q.migrateV2()
}

func (q *Queue) needsV2Migration() (bool, error) {
	var needs bool
	err := q.opt.DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(versionBucket))
		if b == nil {
			needs = true
			return nil
		}
		v := b.Get([]byte("version"))
		if v == nil {
			needs = true
			return nil
		}
		vi, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil || vi <= 1 {
			needs = true
		}
		return nil
	})
	return needs, err
}

func (q *Queue) startBackgroundWorkers() {
	go q.runGCLoop()
	go q.watchGracefulStop()
}

func (q *Queue) runGCLoop() {
	q.clearOrphans()
	for {
		q.gc()
		time.Sleep(120 * time.Second)
	}
}

func (q *Queue) watchGracefulStop() {
	<-q.opt.GracefulStop
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.isIdle() {
		go q.events.Close()
	}
}

func (q *Queue) isIdle() bool {
	return len(q.finalizers) == 0 && len(q.active) == 0
}

func (q *Queue) init() error {
	var err error
	q.initOnce.Do(func() {
		err = q.opt.DB.Update(func(tx *bolt.Tx) error {
			_, err := tx.CreateBucketIfNotExists([]byte(recordsBucket))
			return err
		})
	})
	return err
}

func (q *Queue) leaseID(ref string) string {
	return "ref_" + ref
}
