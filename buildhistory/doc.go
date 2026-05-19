// Package buildhistory persists build records, streams build-history events to
// control-plane clients, and stores or replays solve status (logs and metrics).
//
// Use NewQueue to construct the primary facade. Subpackages provide focused APIs:
//   - filter: query filters for Listen requests
package buildhistory
