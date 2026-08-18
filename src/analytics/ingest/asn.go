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
type asnRange struct {
	start [16]byte
	end   [16]byte
	asn   uint32
}

type asnTable struct {
	ranges []asnRange
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

	t := &asnTable{ranges: make([]asnRange, 0, 1<<19)}
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
		t.ranges = append(t.ranges, asnRange{start: start.As16(), end: end.As16(), asn: uint32(asn)})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(t.ranges) == 0 {
		return nil, fmt.Errorf("%s: no routed ranges", path)
	}
	// The upstream file is sorted within each family, but v4-mapped rows
	// must interleave with low v6 space for the binary search to hold.
	sort.Slice(t.ranges, func(i, j int) bool {
		return bytes.Compare(t.ranges[i].start[:], t.ranges[j].start[:]) < 0
	})
	return t, nil
}

// lookup returns the AS number originating a (v4-mapped or native v6)
// address, or 0 when the address is unrouted or the table is absent.
func (t *asnTable) lookup(a netip.Addr) uint32 {
	if t == nil || !a.IsValid() {
		return 0
	}
	k := a.As16()
	i := sort.Search(len(t.ranges), func(i int) bool {
		return bytes.Compare(t.ranges[i].start[:], k[:]) > 0
	}) - 1
	if i < 0 || bytes.Compare(t.ranges[i].end[:], k[:]) < 0 {
		return 0
	}
	return t.ranges[i].asn
}
