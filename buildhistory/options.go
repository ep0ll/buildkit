package buildhistory

import (
	"context"
	"time"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/cmd/buildkitd/config"
	containerdsnapshot "github.com/moby/buildkit/snapshot/containerd"
	"github.com/moby/buildkit/util/db"
	"github.com/moby/buildkit/util/leaseutil"
)

// Hooks are optional callbacks invoked during queue operations.
type Hooks struct {
	OnEvent      func(context.Context, *controlapi.BuildHistoryEvent) error
	BeforeDelete func(context.Context, string) error
}

// QueueOpt configures a build-history queue.
type QueueOpt struct {
	DB             db.Transactor
	LeaseManager   *leaseutil.Manager
	ContentStore   *containerdsnapshot.Store
	CleanConfig    *config.HistoryConfig
	GarbageCollect func(context.Context) error
	GracefulStop   <-chan struct{}
	Hooks          Hooks
}

func defaultCleanConfig() *config.HistoryConfig {
	return &config.HistoryConfig{
		MaxAge:     config.Duration{Duration: 48 * time.Hour},
		MaxEntries: 50,
	}
}
