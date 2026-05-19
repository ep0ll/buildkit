// Package history re-exports buildhistory for backward compatibility.
package history

import (
	"github.com/moby/buildkit/buildhistory"
	"github.com/moby/buildkit/buildhistory/filter"
)

type (
	Queue              = buildhistory.Queue
	QueueOpt           = buildhistory.QueueOpt
	Hooks              = buildhistory.Hooks
	StatusImportResult = buildhistory.StatusImportResult
)

var NewQueue = buildhistory.NewQueue

// FilterEvents applies history query filters (deprecated: use filter.Events).
var FilterEvents = filter.Events
