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
//   - AttrBuildSecretAliases defines name mappings for forwarded secrets.
//     The value is a comma-separated list of "child=parent" pairs, e.g.
//     "db=prod_db,token=ci_token". The nested build may then fetch the
//     secret under the child name and transparently receives the value
//     stored under the parent name. The parent name must also appear in
//     AttrBuildSecrets (or AttrBuildSecretsFull must be true).
//
// If neither attribute is set, the nested build receives no secrets at
// all: it must not silently inherit the full secret set of its parent.
const (
	AttrBuildSecrets      = "llb.build.secrets"
	AttrBuildSecretsFull  = "llb.build.secrets-full"
	AttrBuildSecretAliases = "llb.build.secrets.aliases"
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

	aliases := parseSecretAliases(b.op.Attrs[AttrBuildSecretAliases])
	return secrets.NewScope(allowed, full, aliases)
}

// parseSecretAliases parses a comma-separated list of "child=parent" alias
// declarations from AttrBuildSecretAliases. Malformed entries are silently
// skipped so that a single typo does not block the entire build.
func parseSecretAliases(v string) map[string]string {
	if v == "" {
		return nil
	}
	aliases := make(map[string]string)
	for _, pair := range strings.Split(v, ",") {
		pair = strings.TrimSpace(pair)
		child, parent, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		child = strings.TrimSpace(child)
		parent = strings.TrimSpace(parent)
		if child != "" && parent != "" {
			aliases[child] = parent
		}
	}
	if len(aliases) == 0 {
		return nil
	}
	return aliases
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

	// Compute the effective scope for the nested build by intersecting the
	// parent's scope (if any) with what this BuildOp declares. This ensures
	// access can only shrink through nesting, never grow.
	childScope := b.secretsScope()
	if parent, ok := secrets.ScopeFromContext(ctx); ok {
		childScope = secrets.Intersect(parent, childScope)
	}

	// Layer 1 — context: stamp the scope on ctx so any op inside the nested
	// build that receives this context already has the restriction applied.
	// This covers the common case where ctx is properly propagated.
	ctx = secrets.WithScope(ctx, childScope)

	newRes, err := b.b.Solve(ctx, frontend.SolveRequest{
		Definition:  def.ToPB(),
		SecretScope: childScope, // Layer 2 — transport: bridge re-stamps ctx even when ops use a fresh context
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
