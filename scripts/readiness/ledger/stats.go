package ledger

import (
	"math"
	"sort"
)

// LatencyStats 毫秒
type LatencyStats struct {
	N   int     `json:"n"`
	P50 float64 `json:"p50_ms"`
	P95 float64 `json:"p95_ms"`
	P99 float64 `json:"p99_ms"`
	Max float64 `json:"max_ms"`
}

// Percentiles 最近秩法
func Percentiles(ns []int64) LatencyStats {
	if len(ns) == 0 {
		return LatencyStats{}
	}
	s := append([]int64(nil), ns...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	at := func(p float64) float64 {
		i := int(math.Ceil(p*float64(len(s)))) - 1
		if i < 0 {
			i = 0
		}
		return float64(s[i]) / 1e6
	}
	return LatencyStats{N: len(s), P50: at(.50), P95: at(.95), P99: at(.99), Max: float64(s[len(s)-1]) / 1e6}
}

// Timing 是从账本推导的时序指标。测量点：
//
//	ToStart  = 首次提交调用前 → 首次 Processor 开始
//	ToEnd    = 首次提交调用前 → 首次 Processor 返回（不含 Ack）
//	ToFinal  = 首次提交调用前 → 对账端首次观察到完成（含轮询间隔，是 Ack 时刻的上界而非 Ack 时刻本身）
//
// 吞吐 = 观察到完成的唯一ID数 / (最早提交前 → 最后一次完成观察)。
type Timing struct {
	ToStart       LatencyStats `json:"submit_to_process_start"`
	ToEnd         LatencyStats `json:"submit_to_process_end"`
	ToFinal       LatencyStats `json:"submit_to_final_observed"`
	MaxObsUBMs    float64      `json:"max_observation_upper_bound_ms"`
	DoneObserved  int          `json:"done_observed"`
	WindowSeconds float64      `json:"window_seconds"`
	PerSecond     float64      `json:"throughput_per_s"`
}

// ComputeTiming 只使用 accepted 提交与 done 的最终观察
func ComputeTiming(recs []Rec) Timing {
	firstTB := map[string]int64{}
	firstStart := map[string]int64{}
	firstEnd := map[string]int64{}
	finalAt := map[string]int64{}
	var maxUB, lastFinal, minTB int64
	for _, r := range recs {
		switch r.K {
		case KSubmit:
			if r.Res != ResAccepted {
				continue
			}
			if v, ok := firstTB[r.ID]; !ok || r.TB < v {
				firstTB[r.ID] = r.TB
			}
			if minTB == 0 || r.TB < minTB {
				minTB = r.TB
			}
		case KProcStart:
			if _, ok := firstStart[r.ID]; !ok {
				firstStart[r.ID] = r.T
			}
		case KProcEnd:
			if _, ok := firstEnd[r.ID]; !ok {
				firstEnd[r.ID] = r.T
			}
		case KFinal:
			if r.State != StateDone || r.Err != "" {
				continue
			}
			if _, ok := finalAt[r.ID]; !ok {
				finalAt[r.ID] = r.T
				if r.T > lastFinal {
					lastFinal = r.T
				}
				if r.UB > maxUB {
					maxUB = r.UB
				}
			}
		}
	}
	var a, b, c []int64
	for id, tb := range firstTB {
		if t, ok := firstStart[id]; ok {
			a = append(a, t-tb)
		}
		if t, ok := firstEnd[id]; ok {
			b = append(b, t-tb)
		}
		if t, ok := finalAt[id]; ok {
			c = append(c, t-tb)
		}
	}
	tm := Timing{ToStart: Percentiles(a), ToEnd: Percentiles(b), ToFinal: Percentiles(c), MaxObsUBMs: float64(maxUB) / 1e6, DoneObserved: len(finalAt)}
	if lastFinal > minTB && minTB > 0 {
		tm.WindowSeconds = float64(lastFinal-minTB) / 1e9
		tm.PerSecond = float64(len(finalAt)) / tm.WindowSeconds
	}
	return tm
}
