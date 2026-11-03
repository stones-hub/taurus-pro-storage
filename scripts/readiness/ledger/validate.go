package ledger

import (
	"fmt"
	"sort"
	"strings"
)

// Violation 是一条校验违规。任何违规都使场景失败。
type Violation struct {
	Code   string `json:"code"`
	ID     string `json:"id,omitempty"`
	Detail string `json:"detail,omitempty"`
}

func (v Violation) String() string {
	s := v.Code
	if v.ID != "" {
		s += " id=" + v.ID
	}
	if v.Detail != "" {
		s += ": " + v.Detail
	}
	return s
}

// Expect 描述场景对账本的期望，随账本一起落盘（<scenario>.expect.json），
// 以便 `gate verify` 离线、独立地重新校验。
type Expect struct {
	// AllowRedelivery 为 false 时，同一ID出现第二次 Processor 调用即违规；
	// 为 true（故障场景）时允许，但会计数进报告。
	AllowRedelivery bool `json:"allow_redelivery"`
	// RedeliveryNeedsUnfinished 为 true 时，重复调用必须有“此前调用未结束”（进程被杀）来解释。
	RedeliveryNeedsUnfinished bool `json:"redelivery_needs_unfinished,omitempty"`
	// AllowUnfinished 允许存在没有 proc_end 的调用（被 SIGKILL 的进程）
	AllowUnfinished bool `json:"allow_unfinished,omitempty"`
	// MustNotProcess 这些ID不应被 Processor 执行（已完成的墓碑任务、不在快照内的任务等）
	MustNotProcess []string `json:"must_not_process,omitempty"`
	// ExpectedLost 被接受但预期不会出现（备份之后的写入）。它们不得被处理，也不要求完成。
	ExpectedLost []string `json:"expected_lost,omitempty"`
	// ExpectedFailed 预期最终处于失败留存的ID（要求有 failed 的 final，且无需 done）
	ExpectedFailed []string `json:"expected_failed,omitempty"`
	// ExpectCalls 由场景脚本决定调用次数的ID（重试/panic/重放/墓碑到期后重新提交）：Processor 调用次数必须
	// 恰好等于该值，否则 call_count_mismatch；这些按脚本发生的重复调用不算 unexpected_duplicate。
	// 不在表内的ID仍按 AllowRedelivery 规则处理。
	ExpectCalls map[string]int `json:"expect_calls,omitempty"`
	// RequiredChecks 必须在账本里出现且 pass=true 的 check 名。缺少即 missing_required_check：
	// 防止“关键断言根本没执行”被当成通过（例如对账步骤被删除/提前返回）。
	RequiredChecks []string `json:"required_checks,omitempty"`
	// PreExisting 在账本 submit 之前就已完成/失败的ID（种子数据），其 final 不要求，也不得被处理
	// （与 MustNotProcess 配合使用）；它们仍要求出现在 submit 中，避免成为“未知ID”。
	// 此字段仅用于说明，不改变校验逻辑。
	Note string `json:"note,omitempty"`
}

// Report 是校验结果与派生统计
type Report struct {
	Violations []Violation `json:"violations"`

	SubmitAttempts  int `json:"submit_attempts"`
	UniqueSubmitted int `json:"unique_submitted"`
	Accepted        int `json:"accepted_ids"`
	Rejected        int `json:"rejected_only_ids"`
	Uncertain       int `json:"uncertain_only_ids"`

	ProcCalls       int `json:"proc_calls"`            // 总执行次数
	UniqueExecuted  int `json:"unique_executed"`       // 唯一ID数
	Duplicates      int `json:"duplicate_calls"`       // 总执行 - 唯一
	ScriptedRepeats int `json:"scripted_repeat_calls"` // Duplicates 中由 ExpectCalls 脚本解释的部分
	UnfinishedCalls int `json:"unfinished_calls"`
	Confirmed       int `json:"confirmed_done"`
	ConfirmedFailed int `json:"confirmed_failed"`
}

// Ok 无违规
func (r *Report) Ok() bool { return len(r.Violations) == 0 }

// Summary 违规摘要（最多 limit 条）
func (r *Report) Summary(limit int) string {
	var b strings.Builder
	for i, v := range r.Violations {
		if i >= limit {
			fmt.Fprintf(&b, "... and %d more", len(r.Violations)-limit)
			break
		}
		b.WriteString(v.String())
		b.WriteByte('\n')
	}
	return b.String()
}

func sortByTime(rs []Rec) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].T < rs[j].T })
}

type idInfo struct {
	shas     map[string]bool
	accepted bool
	rejected bool
	uncert   bool
	starts   []Rec
	ends     map[string]Rec // call -> end
	finals   []Rec
}

// Validate 对账本记录做严格校验。纯函数，不访问 Redis。
//
// 规则（节选）：每个被接受的ID都必须有 Processor 调用且有 done/failed 的最终观察；
// 未知ID、载荷不符、未预期重复、漏完成、查询失败（check 带 err、final 带 err）均为违规。
func Validate(recs []Rec, exp Expect) *Report {
	rep := &Report{Violations: []Violation{}}
	add := func(code, id, f string, a ...any) {
		rep.Violations = append(rep.Violations, Violation{Code: code, ID: id, Detail: fmt.Sprintf(f, a...)})
	}
	set := func(xs []string) map[string]bool {
		m := make(map[string]bool, len(xs))
		for _, x := range xs {
			m[x] = true
		}
		return m
	}
	noProc, lost, wantFailed := set(exp.MustNotProcess), set(exp.ExpectedLost), set(exp.ExpectedFailed)

	ids := map[string]*idInfo{}
	get := func(id string) *idInfo {
		x := ids[id]
		if x == nil {
			x = &idInfo{shas: map[string]bool{}, ends: map[string]Rec{}}
			ids[id] = x
		}
		return x
	}
	known := map[string]string{} // submit 声明的 sha
	lens := map[string]int{}
	var procRecs []Rec

	for _, r := range recs {
		switch r.K {
		case KSubmit:
			rep.SubmitAttempts++
			if r.ID == "" || r.Sha == "" || r.Len <= 0 {
				add("bad_record", r.ID, "submit without id/sha/len")
				continue
			}
			x := get(r.ID)
			if prev, ok := known[r.ID]; ok && prev != r.Sha {
				add("submit_sha_conflict", r.ID, "payload sha changed between attempts")
			}
			known[r.ID] = r.Sha
			lens[r.ID] = r.Len
			x.shas[r.Sha] = true
			switch r.Res {
			case ResAccepted:
				x.accepted = true
			case ResRejected:
				x.rejected = true
			case ResUncertain:
				x.uncert = true
			default:
				add("bad_record", r.ID, "submit with unknown result %q", r.Res)
			}
		case KProcStart, KProcEnd:
			procRecs = append(procRecs, r)
		case KFinal:
			if r.ID == "" {
				add("bad_record", "", "final without id")
				continue
			}
			if r.Err != "" {
				add("query_error", r.ID, "final observation carries error: %s", r.Err)
				continue
			}
			get(r.ID).finals = append(get(r.ID).finals, r)
		case KCheck:
			switch {
			case r.Err != "":
				add("query_error", "", "check %q failed to run: %s", r.Name, r.Err)
			case r.Pass == nil:
				add("bad_record", "", "check %q without pass flag", r.Name)
			case !*r.Pass:
				add("check_failed", "", "check %q: %s", r.Name, r.Detail)
			}
		case KEvent:
		default:
			add("bad_record", r.ID, "unknown record kind %q", r.K)
		}
	}

	// 空账本（没有任何记录或没有任何提交）不是“无违规”，而是失败
	if len(recs) == 0 {
		add("empty_ledger", "", "ledger has no records at all")
	} else if rep.SubmitAttempts == 0 {
		add("empty_ledger", "", "ledger has %d records but no submit record", len(recs))
	}
	passed := map[string]bool{}
	for _, r := range recs {
		if r.K == KCheck && r.Err == "" && r.Pass != nil && *r.Pass {
			passed[r.Name] = true
		}
	}
	for _, name := range exp.RequiredChecks {
		if !passed[name] {
			add("missing_required_check", "", "required check %q never recorded as passed", name)
		}
	}

	// proc 记录：要求先已有 submit（未知ID），载荷摘要一致
	for _, r := range procRecs {
		if r.ID == "" || r.Call == "" {
			add("bad_record", r.ID, "%s without id/call", r.K)
			continue
		}
		x, ok := ids[r.ID]
		if !ok || len(x.shas) == 0 {
			add("unknown_id", r.ID, "%s for an id that was never submitted", r.K)
			continue
		}
		if r.K == KProcStart {
			rep.ProcCalls++
			if r.POK == nil || !*r.POK {
				add("payload_not_ok", r.ID, "call %s received a payload that does not regenerate from its header", r.Call)
			}
			if !x.shas[r.Sha] || r.Len != lens[r.ID] {
				add("payload_mismatch", r.ID, "call %s sha=%.12s len=%d differs from submission", r.Call, r.Sha, r.Len)
			}
			x.starts = append(x.starts, r)
		} else {
			if _, dup := x.ends[r.Call]; dup {
				add("bad_record", r.ID, "call %s ended twice", r.Call)
			}
			x.ends[r.Call] = r
		}
	}

	names := make([]string, 0, len(ids))
	for id := range ids {
		names = append(names, id)
	}
	sort.Strings(names)
	// 脚本期望里的ID必须真的被提交过：否则期望表与账本对不上（例如账本被截掉），不能静默忽略
	for id := range exp.ExpectCalls {
		if x, ok := ids[id]; !ok || len(x.shas) == 0 {
			add("expect_calls_unknown_id", id, "id has an expected call count but was never submitted")
		}
	}

	for _, id := range names {
		x := ids[id]
		rep.UniqueSubmitted++
		switch {
		case x.accepted:
			rep.Accepted++
		case x.rejected:
			rep.Rejected++
		case x.uncert:
			rep.Uncertain++
		}
		started := map[string]bool{}
		for _, s := range x.starts {
			started[s.Call] = true
		}
		for c := range x.ends {
			if !started[c] {
				add("proc_end_without_start", id, "call %s", c)
			}
		}
		unfinished := 0
		for _, s := range x.starts {
			if _, ok := x.ends[s.Call]; !ok {
				unfinished++
			}
		}
		rep.UnfinishedCalls += unfinished
		if unfinished > 0 && !exp.AllowUnfinished {
			add("start_without_end", id, "%d call(s) never returned", unfinished)
		}
		wantCalls, scripted := exp.ExpectCalls[id]
		if scripted && len(x.starts) != wantCalls {
			add("call_count_mismatch", id, "processor called %d time(s), script expects exactly %d", len(x.starts), wantCalls)
		}
		if n := len(x.starts); n > 0 {
			rep.UniqueExecuted++
			rep.Duplicates += n - 1
			if scripted {
				rep.ScriptedRepeats += max(min(n, wantCalls)-1, 0)
			} else if n > 1 {
				if !exp.AllowRedelivery {
					add("unexpected_duplicate", id, "processed %d times in a scenario that forbids redelivery", n)
				} else if exp.RedeliveryNeedsUnfinished && unfinished < n-1 {
					add("unexplained_redelivery", id, "%d calls but only %d unfinished earlier calls", n, unfinished)
				}
			}
		}
		if noProc[id] && len(x.starts) > 0 {
			add("process_forbidden", id, "id marked must-not-process was executed %d time(s)", len(x.starts))
		}
		if lost[id] && len(x.starts) > 0 {
			add("process_forbidden", id, "expected-lost id was executed %d time(s)", len(x.starts))
		}
		if !x.accepted && !x.uncert && x.rejected && len(x.starts) > 0 {
			add("rejected_but_processed", id, "definitively rejected submit but %d execution(s)", len(x.starts))
		}

		// 最终状态：被接受的ID（除预期丢失/禁止处理的）必须被执行并最终确认
		wantProc := x.accepted && !lost[id] && !noProc[id]
		if wantProc && len(x.starts) == 0 {
			add("accepted_not_processed", id, "accepted but the processor never ran")
		}
		if x.accepted && !lost[id] {
			want := StateDone
			if wantFailed[id] {
				want = StateFailed
			}
			got := ""
			for _, f := range x.finals {
				if f.State == StateDigestMismatch {
					add("tombstone_digest_mismatch", id, "completion record digest differs from submitted payload")
				}
				if f.State == want {
					got = f.State
				}
			}
			switch got {
			case StateDone:
				rep.Confirmed++
			case StateFailed:
				rep.ConfirmedFailed++
			default:
				add("accepted_not_confirmed", id, "no %q observation for an accepted id", want)
			}
		}
		if !x.accepted && !x.uncert && len(x.finals) > 0 {
			add("final_unknown", id, "completion observed for an id with no accepted submit")
		}
	}
	return rep
}
