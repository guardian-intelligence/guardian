package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
)

// asnTable resolves a client address to its originating AS number from a
// BGP-derived range snapshot (iptoasn.com combined TSV — public routing
// data, mirrored as a pinned repo release and baked into this image; see
// src/infrastructure/runbooks/ip2asn-refresh.md). Lookups are in-memory so
// the write path has no runtime dependency, and rows record what the
// routing table said at event time.
type asnTable struct {
	starts [][16]byte
	ends   [][16]byte
	asns   []uint32
}

// loadASNTable parses the gzipped "start\tend\tas_number\tcountry\tname"
// TSV. IPv4 rows are stored v4-mapped (Addr.As16) because every client
// address is normalized with mapToV6 before lookup — raw v4 keys would
// never match. Unrouted ranges (AS0) are dropped.
func loadASNTable(path string) (*asnTable, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer gz.Close()

	t := &asnTable{}
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	line := 0
	for sc.Scan() {
		line++
		fields := strings.SplitN(sc.Text(), "\t", 4)
		if len(fields) < 3 {
			return nil, fmt.Errorf("%s:%d: want >=3 tab-separated fields, got %d", path, line, len(fields))
		}
		asn, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: as_number: %w", path, line, err)
		}
		if asn == 0 {
			continue
		}
		start, err := netip.ParseAddr(fields[0])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: range_start: %w", path, line, err)
		}
		end, err := netip.ParseAddr(fields[1])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: range_end: %w", path, line, err)
		}
		t.starts = append(t.starts, start.As16())
		t.ends = append(t.ends, end.As16())
		t.asns = append(t.asns, uint32(asn))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(t.asns) == 0 {
		return nil, fmt.Errorf("%s: no routed ranges", path)
	}
	// The upstream file is sorted within each family, but v4-mapped rows
	// must interleave with low v6 space for the binary search to hold.
	sort.Sort(t)
	return t, nil
}

func (t *asnTable) Len() int { return len(t.asns) }
func (t *asnTable) Less(i, j int) bool {
	return bytes.Compare(t.starts[i][:], t.starts[j][:]) < 0
}
func (t *asnTable) Swap(i, j int) {
	t.starts[i], t.starts[j] = t.starts[j], t.starts[i]
	t.ends[i], t.ends[j] = t.ends[j], t.ends[i]
	t.asns[i], t.asns[j] = t.asns[j], t.asns[i]
}

// lookup returns the AS number originating a (v4-mapped or native v6)
// address, or 0 when the address is unrouted or the table is absent.
func (t *asnTable) lookup(a netip.Addr) uint32 {
	if t == nil || !a.IsValid() {
		return 0
	}
	k := a.As16()
	i := sort.Search(len(t.starts), func(i int) bool {
		return bytes.Compare(t.starts[i][:], k[:]) > 0
	}) - 1
	if i < 0 || bytes.Compare(t.ends[i][:], k[:]) < 0 {
		return 0
	}
	return t.asns[i]
}
