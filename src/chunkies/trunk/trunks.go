package trunk

import (
	"context"
	"errors"
)

// Directory resolves which backend address serves a (game, chunk) pair.
// Resolution lives inside the trunk so callers state the pair exactly
// once, at Attach time; the trunk owns everything after that.
type Directory interface {
	Lookup(game, chunk string) (addr string, ok bool)
}

// ErrNoRoute means the directory names no backend for the pair — a
// routing gap, distinct from failing to reach a backend it does name.
var ErrNoRoute = errors.New("no route to chunk")

// AttachSpec is everything an attachment states, stated once.
type AttachSpec struct {
	Game      string
	Chunk     string
	Sub       string
	Role      string
	Remote    string
	SinceSeq  int64
	SinceTick uint64
}

// Trunks is the dial side of the trunk fabric: directory resolution and
// the shared-connection pool behind a single Attach call.
type Trunks struct {
	pool *Pool
	dir  Directory
}

func NewTrunks(key []byte, dir Directory, hooks Hooks) *Trunks {
	return &Trunks{pool: NewPool(key, hooks), dir: dir}
}

// Attach opens one attachment to the authority serving spec's (game,
// chunk) pair.
func (t *Trunks) Attach(ctx context.Context, spec AttachSpec) (*Attachment, error) {
	addr, ok := t.dir.Lookup(spec.Game, spec.Chunk)
	if !ok {
		return nil, ErrNoRoute
	}
	return t.pool.Open(ctx, addr, Open{
		Game: spec.Game, Chunk: spec.Chunk, Sub: spec.Sub, Role: spec.Role,
		Remote: spec.Remote, SinceSeq: spec.SinceSeq, SinceTick: spec.SinceTick,
	})
}
