package main

import (
	"os"
	"testing"
)

func TestParseProcStatFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/proc_stat")
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseProcStat(raw)
	if err != nil {
		t.Fatal(err)
	}
	// user+nice+system+idle+iowait+irq+softirq+steal of the fixture's
	// aggregate line; guest fields excluded.
	want := cpuTotals{idle: 40321277758, total: 41648908912}
	if got != want {
		t.Fatalf("parseProcStat = %+v, want %+v", got, want)
	}
}

func TestParseMeminfoFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/proc_meminfo")
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseMeminfo(raw)
	if err != nil {
		t.Fatal(err)
	}
	// (65437016 - 23489288) * 10000 / 65437016
	if want := uint32(6410); got != want {
		t.Fatalf("parseMeminfo = %d bp, want %d", got, want)
	}
}

func TestParseProcStatErrors(t *testing.T) {
	for name, data := range map[string]string{
		"empty":        "",
		"noAggregate":  "cpu0 1 2 3 4 5 6 7 8\n",
		"shortLine":    "cpu  1 2 3 4\n",
		"garbageField": "cpu  1 2 x 4 5 6 7 8\n",
	} {
		if _, err := parseProcStat([]byte(data)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestParseMeminfoErrors(t *testing.T) {
	for name, data := range map[string]string{
		"empty":       "",
		"noAvailable": "MemTotal: 100 kB\n",
		"zeroTotal":   "MemTotal: 0 kB\nMemAvailable: 0 kB\n",
		"garbage":     "MemTotal: x kB\nMemAvailable: 1 kB\n",
	} {
		if _, err := parseMeminfo([]byte(data)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestParseMeminfoAvailableAboveTotalClamps(t *testing.T) {
	got, err := parseMeminfo([]byte("MemTotal: 100 kB\nMemAvailable: 150 kB\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("got %d bp, want 0", got)
	}
}

func TestCpuBusyBp(t *testing.T) {
	cases := []struct {
		name       string
		prev, cur  cpuTotals
		want       uint32
		wantSignal bool
	}{
		{"halfBusy", cpuTotals{idle: 100, total: 200}, cpuTotals{idle: 150, total: 300}, 5000, true},
		{"allIdle", cpuTotals{idle: 100, total: 200}, cpuTotals{idle: 200, total: 300}, 0, true},
		{"allBusy", cpuTotals{idle: 100, total: 200}, cpuTotals{idle: 100, total: 300}, 10000, true},
		{"noProgress", cpuTotals{idle: 100, total: 200}, cpuTotals{idle: 100, total: 200}, 0, false},
		{"totalWrapped", cpuTotals{idle: 100, total: 200}, cpuTotals{idle: 100, total: 150}, 0, false},
		{"idleWrapped", cpuTotals{idle: 100, total: 200}, cpuTotals{idle: 50, total: 300}, 0, false},
		{"idleOutpacesTotal", cpuTotals{idle: 100, total: 200}, cpuTotals{idle: 350, total: 300}, 0, false},
		{"firstSampleZeroPrev", cpuTotals{}, cpuTotals{}, 0, false},
	}
	for _, c := range cases {
		got, ok := cpuBusyBp(c.prev, c.cur)
		if ok != c.wantSignal || got != c.want {
			t.Errorf("%s: cpuBusyBp = (%d, %v), want (%d, %v)", c.name, got, ok, c.want, c.wantSignal)
		}
	}
}

func TestProcfsTickerPrimesBeforeEmitting(t *testing.T) {
	next := procfsTicker("testdata/proc_stat", "testdata/proc_meminfo")
	if _, _, ok := next(); ok {
		t.Fatal("first call must only prime the jiffies baseline")
	}
	// Identical readings: no elapsed jiffies, still no signal.
	if _, _, ok := next(); ok {
		t.Fatal("second call over frozen counters must carry no signal")
	}
}
