package ledger

import "testing"

func TestComputeTiming(t *testing.T) {
	rs := []Rec{
		{K: KSubmit, T: 5, ID: "a", Res: ResAccepted, TB: 1_000_000_000},
		{K: KProcStart, T: 1_100_000_000, ID: "a"},
		{K: KProcEnd, T: 1_200_000_000, ID: "a"},
		{K: KFinal, T: 2_000_000_000, ID: "a", State: StateDone, UB: 50_000_000},
		{K: KProcStart, T: 1_500_000_000, ID: "a"}, // 重复调用不改变首次延迟
	}
	tm := ComputeTiming(rs)
	if tm.ToStart.P50 != 100 || tm.ToEnd.P50 != 200 || tm.ToFinal.P50 != 1000 {
		t.Fatalf("%+v", tm)
	}
	if tm.PerSecond != 1 || tm.MaxObsUBMs != 50 {
		t.Fatalf("throughput must use final-observed time: %+v", tm)
	}
}
