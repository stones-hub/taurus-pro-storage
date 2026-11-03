package main

import (
	"strings"
	"testing"
)

func TestSoakBreachesTripBelowResourceCaps(t *testing.T) {
	l := defaultSoakLimits()
	// 阈值必须严格低于资源上限：maxmemory 128mb、容器 384MiB、ListFailed 单次 1000
	if l.RedisUsedBytes >= 128<<20 || l.ContainerBytes >= 384<<20 || l.MaxFailed >= 1000 {
		t.Fatalf("limits not below caps: %+v", l)
	}
	ok := soakSample{RedisUsed: 10 << 20, ContainerMem: 50 << 20, GoSys: 64 << 20, FreeDisk: 10 << 30, AOFStatus: "ok"}
	if b := soakBreaches(ok, l); len(b) != 0 {
		t.Fatalf("healthy sample breached: %v", b)
	}
	cases := map[string]func(*soakSample){
		"redis":       func(x *soakSample) { x.RedisUsed = l.RedisUsedBytes },
		"container":   func(x *soakSample) { x.ContainerMem = l.ContainerBytes },
		"gate":        func(x *soakSample) { x.GoSys = l.GateSysBytes },
		"disk":        func(x *soakSample) { x.FreeDisk = l.MinFreeDiskBytes - 1 },
		"outstanding": func(x *soakSample) { x.Outstanding = l.MaxOutstanding + 1 },
		"failed":      func(x *soakSample) { x.Q.Failed = l.MaxFailed + 1 },
		"ledger":      func(x *soakSample) { x.LedgerBytes = l.MaxLedgerBytes + 1 },
		"gap":         func(x *soakSample) { x.GapMs = l.MaxSampleGap + 1 },
		"aof":         func(x *soakSample) { x.AOFStatus = "err" },
	}
	for name, mut := range cases {
		x := ok
		mut(&x)
		if b := soakBreaches(x, l); len(b) != 1 {
			t.Errorf("%s: want exactly one breach, got %v", name, b)
		}
	}
}

func TestSoakClassesDisjointAndPayloadBounded(t *testing.T) {
	seen := map[string]int{}
	for i := 0; i < 18000; i++ {
		seen[soakClass(i)]++
		if sz := soakSize(i); sz < 256 || sz > 4096 {
			t.Fatalf("size %d out of 256..4096 for %d", sz, i)
		}
		if soakIndex(soakID(i)) != i {
			t.Fatalf("id round trip %d", i)
		}
	}
	for _, c := range []string{"ok", "replay", "fatal", "dupprobe", "expire", "panic1", "retry1"} {
		if seen[c] == 0 {
			t.Errorf("class %s never selected", c)
		}
	}
	// 失败留存规模（fatal 类）必须低于 ListFailed 单次上限与安全阈值
	if seen["fatal"] >= int(defaultSoakLimits().MaxFailed) {
		t.Fatalf("fatal class %d would hit the failed-retained threshold", seen["fatal"])
	}
	if soakIndex("x-1") != -1 || !strings.HasPrefix(soakID(3), "s-") {
		t.Fatal("index parsing")
	}
}

func TestSoakTrendSlope(t *testing.T) {
	var xs []soakSample
	for i := 0; i < 10; i++ {
		xs = append(xs, soakSample{Phase: "produce", ElapsedS: float64(600 + 60*i), RedisUsed: int64(1000 + 100*i)})
	}
	tr := soakTrend(xs, 300)
	m := tr["redis_used_memory"].(map[string]float64)
	if m["slope_per_min"] < 99.9 || m["slope_per_min"] > 100.1 || m["max"] != 1900 {
		t.Fatalf("trend %+v", m)
	}
}
