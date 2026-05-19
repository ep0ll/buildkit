package history

import (
	"bufio"
	"encoding/binary"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

type statusMetrics struct {
	vertices map[digest.Digest]*vertexProgress
	warnings int
}

type vertexProgress struct {
	cached    bool
	completed bool
}

func newStatusMetrics() *statusMetrics {
	return &statusMetrics{vertices: map[digest.Digest]*vertexProgress{}}
}

func (m *statusMetrics) observe(st *client.SolveStatus) {
	m.warnings += len(st.Warnings)
	for _, vtx := range st.Vertexes {
		v, ok := m.vertices[vtx.Digest]
		if !ok {
			v = &vertexProgress{}
			m.vertices[vtx.Digest] = v
		}
		if vtx.Cached {
			v.cached = true
		}
		if vtx.Completed != nil {
			v.completed = true
		}
	}
}

func (m *statusMetrics) result(desc ocispecs.Descriptor) *StatusImportResult {
	numCached, numCompleted := 0, 0
	for _, v := range m.vertices {
		if v.cached {
			numCached++
		}
		if v.completed {
			numCompleted++
		}
	}
	return &StatusImportResult{
		Descriptor:        desc,
		NumCachedSteps:    numCached,
		NumCompletedSteps: numCompleted,
		NumTotalSteps:     len(m.vertices),
		NumWarnings:       m.warnings,
	}
}

func importStatusFrame(bufW *bufio.Writer, frame []byte, metrics *statusMetrics) error {
	var sr controlapi.StatusResponse
	if err := sr.UnmarshalVT(frame); err != nil {
		return err
	}
	metrics.observe(client.NewSolveStatus(&sr))

	hdr := make([]byte, 4)
	binary.LittleEndian.PutUint32(hdr, uint32(len(frame)))
	if _, err := bufW.Write(hdr); err != nil {
		return err
	}
	_, err := bufW.Write(frame)
	return err
}
