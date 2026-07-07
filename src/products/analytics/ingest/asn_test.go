package main

import (
	"compress/gzip"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

func writeGzTSV(t *testing.T, lines string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ip2asn.tsv.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write([]byte(lines)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// Fixture mirrors the iptoasn combined-TSV shape: v4 and v6 sections,
// unrouted (AS0) holes, AS names with tabs' worth of free text.
const fixtureTSV = "1.0.0.0\t1.0.0.255\t13335\tUS\tCLOUDFLARENET\n" +
	"1.0.1.0\t1.0.3.255\t0\tNone\tNot routed\n" +
	"203.0.113.0\t203.0.113.255\t64496\tZZ\tDOC-AS example\n" +
	"2001:db8::\t2001:db8:ffff:ffff:ffff:ffff:ffff:ffff\t64500\tZZ\tDOC6-AS\n"

func TestLoadASNTableAndLookup(t *testing.T) {
	tab, err := loadASNTable(writeGzTSV(t, fixtureTSV))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(tab.asns); got != 3 {
		t.Fatalf("routed ranges = %d, want 3 (AS0 dropped)", got)
	}

	cases := []struct {
		ip   string
		want uint32
	}{
		{"1.0.0.0", 13335},   // range start inclusive
		{"1.0.0.255", 13335}, // range end inclusive
		{"1.0.1.7", 0},       // unrouted hole
		{"1.0.4.0", 0},       // past the last v4 range
		{"203.0.113.9", 64496},
		{"2001:db8::1", 64500},
		{"2001:db9::1", 0},
		{"0.255.255.255", 0}, // before the first range
	}
	for _, c := range cases {
		// Lookups always see mapToV6-normalized addresses, as in the server.
		addr := mapToV6(netip.MustParseAddr(c.ip))
		if got := tab.lookup(addr); got != c.want {
			t.Errorf("lookup(%s) = %d, want %d", c.ip, got, c.want)
		}
	}
}

func TestASNTableNilAndInvalid(t *testing.T) {
	var tab *asnTable
	if got := tab.lookup(netip.MustParseAddr("1.0.0.1")); got != 0 {
		t.Errorf("nil table lookup = %d, want 0", got)
	}
	full, err := loadASNTable(writeGzTSV(t, fixtureTSV))
	if err != nil {
		t.Fatal(err)
	}
	if got := full.lookup(netip.Addr{}); got != 0 {
		t.Errorf("invalid addr lookup = %d, want 0", got)
	}
}

func TestLoadASNTableRejectsGarbage(t *testing.T) {
	if _, err := loadASNTable(writeGzTSV(t, "not-an-ip\t1.0.0.255\t1\tUS\tX\n")); err == nil {
		t.Error("bad range_start accepted")
	}
	if _, err := loadASNTable(writeGzTSV(t, "1.0.0.0\t1.0.0.255\tNaN\tUS\tX\n")); err == nil {
		t.Error("bad as_number accepted")
	}
	if _, err := loadASNTable(writeGzTSV(t, "1.0.1.0\t1.0.3.255\t0\tNone\tNot routed\n")); err == nil {
		t.Error("all-unrouted table accepted; must fail loud")
	}
}
