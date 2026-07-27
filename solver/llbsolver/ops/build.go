package ops

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/containerd/continuity/fs"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/session/secrets"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/llbsolver/ops/opsutils"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/cachedigest"
	"github.com/moby/buildkit/worker"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

const buildCacheType = "buildkit.build.v0"

// Attribute keys read from pb.BuildOp.Attrs to control which secrets, if
// any, are forwarded to a nested build.
//
//   - AttrBuildSecrets lists the secret IDs (comma-separated) that the
//     nested build is allowed to fetch. Any secret ID not on this list is
//     invisible to the nested build, as if it did not exist.
//   - AttrBuildSecretsFull, when set to "true", disables filtering
//     entirely and forwards every secret available on the session to the
//     nested build. This is an explicit opt-in; it is never the default.
//
// If neither attribute is set, the nested build receives no secrets at
// all: it must not silently inherit the full secret set of its parent.
const (
	AttrBuildSecrets     = "llb.build.secrets"
	AttrBuildSecretsFull = "llb.build.secrets-full"
)

type BuildOp struct {
	op *pb.BuildOp
	b  frontend.FrontendLLBBridge
	v  solver.Vertex
}

var _ solver.Op = &BuildOp{}

func NewBuildOp(v solver.Vertex, op *pb.Op_Build, b frontend.FrontendLLBBridge, _ worker.Worker) (*BuildOp, error) {
	if err := opsutils.Validate(&pb.Op{Op: op}); err != nil {
		return nil, err
	}
	return &BuildOp{
		op: op.Build,
		b:  b,
		v:  v,
	}, nil
}

func (b *BuildOp) CacheMap(ctx context.Context, job solver.JobContext, index int) (*solver.CacheMap, bool, error) {
	dt, err := json.Marshal(struct {
		Type string
		Exec *pb.BuildOp
	}{
		Type: buildCacheType,
		Exec: b.op,
	})
	if err != nil {
		return nil, false, err
	}

	dgst, err := cachedigest.FromBytes(dt, cachedigest.TypeJSON)
	if err != nil {
		return nil, false, err
	}
	return &solver.CacheMap{
		Digest: dgst,
		Deps: make([]struct {
			Selector          digest.Digest
			ComputeDigestFunc solver.ResultBasedCacheFunc
			PreprocessFunc    solver.PreprocessFunc
		}, len(b.v.Inputs())),
	}, true, nil
}

// secretsScope derives the secrets.Scope that should be in effect for the
// nested build, based on this BuildOp's attributes. By default (no
// attributes set) the returned scope allows no secrets whatsoever; callers
// must explicitly allow-list secret IDs, or opt in to full forwarding.
func (b *BuildOp) secretsScope() *secrets.Scope {
	full, _ := strconv.ParseBool(b.op.Attrs[AttrBuildSecretsFull])

	var allowed []string
	if v := b.op.Attrs[AttrBuildSecrets]; v != "" {
		for _, id := range strings.Split(v, ",") {
			id = strings.TrimSpace(id)
			if id != "" {
				allowed = append(allowed, id)
			}
		}
	}

	return secrets.NewScope(allowed, full)
}

func (b *BuildOp) Exec(ctx context.Context, job solver.JobContext, inputs []solver.Result) (outputs []solver.Result, retErr error) {
	if b.op.Builder != int64(pb.LLBBuilder) {
		return nil, errors.Errorf("only LLB builder is currently allowed")
	}

	builderInputs := b.op.Inputs
	llbDef, ok := builderInputs[pb.LLBDefinitionInput]
	if !ok {
		return nil, errors.Errorf("no llb definition input %s found", pb.LLBDefinitionInput)
	}

	i := int(llbDef.Input)
	if i < 0 || i >= len(inputs) {
		return nil, errors.Errorf("invalid index %v", i) // TODO: this should be validated before
	}
	inp := inputs[i]

	ref, ok := inp.Sys().(*worker.WorkerRef)
	if !ok {
		return nil, errors.Errorf("invalid reference for build %T", inp.Sys())
	}

	g := job.Session()
	mount, err := ref.ImmutableRef.Mount(ctx, true, g)
	if err != nil {
		return nil, err
	}

	lm := snapshot.LocalMounter(mount)

	root, err := lm.Mount()
	if err != nil {
		return nil, err
	}

	defer func() {
		if retErr != nil && lm != nil {
			lm.Unmount()
		}
	}()

	fn := pb.LLBDefaultDefinitionFile
	if override, ok := b.op.Attrs[pb.AttrLLBDefinitionFilename]; ok {
		fn = override
	}

	newfn, err := fs.RootPath(root, fn)
	if err != nil {
		return nil, errors.Wrapf(err, "working dir %s points to invalid target", fn)
	}

	f, err := os.Open(newfn)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to open %s", newfn)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, errors.WithStack(err)
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, errors.Errorf("%s is not a regular file", newfn)
	}

	def, err := llb.ReadFrom(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	lm.Unmount()
	lm = nil

	// Scope secret visibility for the nested build. By default the nested
	// build sees no secrets; the outer build must either allow-list a
	// subset via AttrBuildSecrets, or explicitly opt in to full forwarding
	// via AttrBuildSecretsFull. This scope travels with ctx through the
	// nested Solve call and is enforced centrally in
	// session/secrets.GetSecret, so every secret-consuming op (exec
	// mounts, secret env, etc.) in the nested build is covered without
	// needing to be individually aware of it.
	ctx = secrets.WithScope(ctx, b.secretsScope())

	newRes, err := b.b.Solve(ctx, frontend.SolveRequest{
		Definition: def.ToPB(),
	}, g.SessionIterator().NextSession())
	if err != nil {
		return nil, err
	}

	newRes.EachRef(func(ref solver.ResultProxy) error {
		if ref == newRes.Ref {
			return nil
		}
		return ref.Release(context.TODO())
	})

	r, err := newRes.Ref.Result(ctx)
	if err != nil {
		return nil, err
	}

	return []solver.Result{r}, err
}

func (b *BuildOp) Acquire(ctx context.Context) (solver.ReleaseFunc, error) {
	// buildOp itself does not count towards parallelism budget.
	return func() {}, nil
}

func (b *BuildOp) IsProvenanceProvider() {}
