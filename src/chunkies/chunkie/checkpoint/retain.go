package checkpoint

import (
	"fmt"
	"time"

	"github.com/guardian-intelligence/guardian/src/chunkies/chunkie/ticklog"
)

// trimFloor keeps very young WAL segments regardless of coverage — a
// safety margin against a checkpoint racing the record that covers it.
const trimFloor = time.Minute

// Retain enforces the keep-K ring per chunk, honors the epoch guard,
// and trims WAL segments wholly below the oldest KEPT checkpoint.
//
// A ref is removable only when it falls outside the newest keep refs
// AND a proven ref newer than it exists or it shares the newest ref's
// epoch (same-pair state is strictly dominated by newer same-pair
// state). The surviving exception is exactly the design of record's:
// the newest pre-barrier checkpoint is immortal until a post-barrier
// checkpoint has been proven — a crash mid-promotion resumes the old
// pair, and nothing that could resume may ever be trimmed.
func Retain(st Store, wal *ticklog.Log, keep int) error {
	if keep < 2 {
		return fmt.Errorf("checkpoint: keep=%d, ring needs at least 2", keep)
	}
	chunks, err := st.Chunks()
	if err != nil {
		return err
	}
	covered := ticklog.Tips{}
	for _, chunk := range chunks {
		refs, err := st.List(chunk)
		if err != nil {
			return err
		}
		if len(refs) == 0 {
			continue
		}
		// Trim coverage must come from the lineage recovery would replay
		// — the newest. An old-lineage checkpoint's tick can exceed the
		// new lineage's frontier (a rewind starts behind), and publishing
		// it would let Trim delete new-lineage segments replay still
		// needs.
		lineage := refs[0].Lineage
		// The newest proven ref's position, if any (refs are newest-first).
		proven := -1
		for i, r := range refs {
			if r.Proven {
				proven = i
				break
			}
		}
		// Beyond the ring, a ref survives only as the newest of an epoch
		// no proven ref has superseded — the crash-mid-promotion resume
		// point. seen marks epochs already represented by a newer ref.
		// Within one epoch the domination is deliberate: an unproven
		// newer checkpoint outranks an older proven one — same module
		// pair, more covered history, and the ring keeps fallbacks.
		seen := map[uint32]bool{}
		var oldestKept *Ref
		for i, r := range refs {
			removable := i >= keep &&
				((proven >= 0 && proven < i) || seen[r.Epoch])
			seen[r.Epoch] = true
			if removable {
				if err := st.Remove(r); err != nil {
					return err
				}
				continue
			}
			if r.Lineage == lineage {
				kept := r
				oldestKept = &kept
			}
		}
		if oldestKept != nil {
			covered[chunk] = ticklog.Tip{Tick: oldestKept.Tick, Seq: oldestKept.Seq}
		}
	}
	if wal != nil && len(covered) > 0 {
		if _, err := wal.Trim(covered, trimFloor); err != nil {
			return err
		}
	}
	return nil
}
