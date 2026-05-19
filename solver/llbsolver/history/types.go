package history

import (
	"context"
	"sync"
	"time"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/cmd/buildkitd/config"
	containerdsnapshot "github.com/moby/buildkit/snapshot/containerd"
	"github.com/moby/buildkit/util/db"
	"github.com/moby/buildkit/util/leaseutil"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	recordsBucket = "_records"
	versionBucket = "_version"
)

// QueueOpt configures a build-history queue.
type QueueOpt struct {
	DB             db.Transactor
	LeaseManager   *leaseutil.Manager
	ContentStore   *containerdsnapshot.Store
	CleanConfig    *config.HistoryConfig
	GarbageCollect func(context.Context) error
	GracefulStop   <-chan struct{}
}

// StatusImportResult holds a persisted status blob and derived step metrics.
type StatusImportResult struct {
	Descriptor        ocispecs.Descriptor
	NumCachedSteps    int
	NumCompletedSteps int
	NumTotalSteps     int
	NumWarnings       int
}

// recordFinalizer coordinates trace export before a record becomes immutable.
type recordFinalizer struct {
	trigger func()
	done    chan struct{}
}

// Queue stores build history in bolt and containerd and broadcasts events.
type Queue struct {
	mu sync.Mutex

	initOnce sync.Once
	opt      QueueOpt

	events *pubsub[*controlapi.BuildHistoryEvent]

	active     map[string]*controlapi.BuildHistoryRecord
	finalizers map[string]*recordFinalizer
	refLocks   map[string]int
	pendingDel map[string]struct{}

	liveStatus map[string]*statusPipeline

	hContentStore *containerdsnapshot.Store
	hLeaseManager *leaseutil.Manager
}

func defaultCleanConfig() *config.HistoryConfig {
	return &config.HistoryConfig{
		MaxAge:     config.Duration{Duration: 48 * time.Hour},
		MaxEntries: 50,
	}
}
