package ledger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// good 构造一个完全合法的小账本：n 个ID，各提交一次、执行一次、观察到完成。
func good(t *testing.T, n int) []Rec {
	t.Helper()
	var rs []Rec
	ts := int64(1000)
	next := func() int64 { ts++; return ts }
	for i := 0; i < n; i++ {
		id := "t" + string(rune('a'+i))
		p, err := MakePayload(id, 256)
		if err != nil {
			t.Fatal(err)
		}
		sha := SHA256Hex(p)
		rs = append(rs,
			Rec{K: KSubmit, T: next(), ID: id, Sha: sha, Len: len(p), Res: ResAccepted, Try: 1},
			Rec{K: KProcStart, T: next(), ID: id, Sha: sha, Len: len(p), Pid: 1, Call: "1:" + id, POK: Bool(true)},
			Rec{K: KProcEnd, T: next(), ID: id, Pid: 1, Call: "1:" + id, Ret: "nil"},
			Rec{K: KFinal, T: next(), ID: id, State: StateDone},
		)
	}
	rs = append(rs, Rec{K: KCheck, T: next(), Name: "quiescent", Pass: Bool(true)})
	return rs
}

func codes(r *Report) string {
	var c []string
	for _, v := range r.Violations {
		c = append(c, v.Code)
	}
	return strings.Join(c, ",")
}

func TestValidatePositive(t *testing.T) {
	r := Validate(good(t, 5), Expect{})
	if !r.Ok() {
		t.Fatalf("clean ledger rejected: %s", r.Summary(10))
	}
	if r.UniqueSubmitted != 5 || r.ProcCalls != 5 || r.UniqueExecuted != 5 || r.Duplicates != 0 || r.Confirmed != 5 {
		t.Fatalf("unexpected stats: %+v", r)
	}
}

// ---- 负例：每一种都必须产生对应违规，证明门禁有效 ----

func TestNegativeUnknownID(t *testing.T) {
	rs := append(good(t, 3), Rec{K: KProcStart, T: 5000, ID: "ghost", Sha: "00", Len: 1, Call: "1:x", POK: Bool(true)})
	if r := Validate(rs, Expect{AllowUnfinished: true}); !strings.Contains(codes(r), "unknown_id") {
		t.Fatalf("unknown id not detected: %s", codes(r))
	}
}

func TestNegativePayloadMismatch(t *testing.T) {
	rs := good(t, 3)
	for i := range rs {
		if rs[i].K == KProcStart && rs[i].ID == "tb" {
			rs[i].Sha = SHA256Hex([]byte("tampered"))
		}
	}
	if r := Validate(rs, Expect{}); !strings.Contains(codes(r), "payload_mismatch") {
		t.Fatalf("wrong payload sha not detected: %s", codes(r))
	}
	rs = good(t, 3)
	for i := range rs {
		if rs[i].K == KProcStart && rs[i].ID == "tb" {
			rs[i].POK = Bool(false)
		}
	}
	if r := Validate(rs, Expect{}); !strings.Contains(codes(r), "payload_not_ok") {
		t.Fatalf("payload_ok=false not detected: %s", codes(r))
	}
}

func TestNegativeMissingCompletion(t *testing.T) {
	var rs []Rec
	for _, r := range good(t, 3) {
		if r.K == KFinal && r.ID == "tb" {
			continue // 漏完成
		}
		rs = append(rs, r)
	}
	if r := Validate(rs, Expect{}); !strings.Contains(codes(r), "accepted_not_confirmed") {
		t.Fatalf("missing completion not detected: %s", codes(r))
	}
	// 漏执行
	rs = nil
	for _, r := range good(t, 3) {
		if (r.K == KProcStart || r.K == KProcEnd) && r.ID == "tb" {
			continue
		}
		rs = append(rs, r)
	}
	if r := Validate(rs, Expect{}); !strings.Contains(codes(r), "accepted_not_processed") {
		t.Fatalf("missing processing not detected: %s", codes(r))
	}
}

func TestNegativeQueryFailureIsNotZero(t *testing.T) {
	rs := append(good(t, 2), Rec{K: KCheck, T: 9000, Name: "pending_count", Err: "dial tcp: connection refused"})
	if r := Validate(rs, Expect{}); !strings.Contains(codes(r), "query_error") {
		t.Fatalf("check query error not detected: %s", codes(r))
	}
	rs = append(good(t, 2), Rec{K: KFinal, T: 9000, ID: "ta", Err: "i/o timeout"})
	if r := Validate(rs, Expect{}); !strings.Contains(codes(r), "query_error") {
		t.Fatalf("final query error not detected: %s", codes(r))
	}
	rs = append(good(t, 2), Rec{K: KCheck, T: 9000, Name: "stream_len", Pass: Bool(false), Detail: "got 3 want 0"})
	if r := Validate(rs, Expect{}); !strings.Contains(codes(r), "check_failed") {
		t.Fatalf("failed check not detected: %s", codes(r))
	}
	rs = append(good(t, 2), Rec{K: KCheck, T: 9000, Name: "no_pass_flag"})
	if r := Validate(rs, Expect{}); !strings.Contains(codes(r), "bad_record") {
		t.Fatalf("check without verdict not detected: %s", codes(r))
	}
}

func TestDuplicatesAppendedAndCounted(t *testing.T) {
	rs := good(t, 2)
	// 再追加一次对 ta 的完整调用（重复执行）
	var sha string
	var ln int
	for _, r := range rs {
		if r.K == KSubmit && r.ID == "ta" {
			sha, ln = r.Sha, r.Len
		}
	}
	rs = append(rs,
		Rec{K: KProcStart, T: 4000, ID: "ta", Sha: sha, Len: ln, Call: "2:1", POK: Bool(true)},
		Rec{K: KProcEnd, T: 4001, ID: "ta", Call: "2:1", Ret: "nil"})
	if r := Validate(rs, Expect{}); !strings.Contains(codes(r), "unexpected_duplicate") {
		t.Fatalf("duplicate not rejected in no-redelivery scenario: %s", codes(r))
	}
	r := Validate(rs, Expect{AllowRedelivery: true})
	if !r.Ok() || r.ProcCalls != 3 || r.UniqueExecuted != 2 || r.Duplicates != 1 {
		t.Fatalf("redelivery must be counted, not overwritten: ok=%v %+v %s", r.Ok(), r, codes(r))
	}
	// 重复必须由“此前未结束的调用”解释
	if r := Validate(rs, Expect{AllowRedelivery: true, RedeliveryNeedsUnfinished: true}); !strings.Contains(codes(r), "unexplained_redelivery") {
		t.Fatalf("unexplained redelivery accepted: %s", codes(r))
	}
}

func TestUnfinishedAndForbidden(t *testing.T) {
	rs := good(t, 2)
	var out []Rec
	for _, r := range rs {
		if r.K == KProcEnd && r.ID == "ta" {
			continue
		}
		out = append(out, r)
	}
	if r := Validate(out, Expect{}); !strings.Contains(codes(r), "start_without_end") {
		t.Fatalf("unfinished call not detected: %s", codes(r))
	}
	if r := Validate(rs, Expect{MustNotProcess: []string{"ta"}}); !strings.Contains(codes(r), "process_forbidden") {
		t.Fatalf("forbidden execution not detected: %s", codes(r))
	}
	if r := Validate(rs, Expect{ExpectedLost: []string{"ta"}}); !strings.Contains(codes(r), "process_forbidden") {
		t.Fatalf("lost-but-executed not detected: %s", codes(r))
	}
}

func TestDigestMismatchAndFinalUnknown(t *testing.T) {
	rs := good(t, 2)
	for i := range rs {
		if rs[i].K == KFinal && rs[i].ID == "ta" {
			rs[i].State = StateDigestMismatch
		}
	}
	if r := Validate(rs, Expect{}); !strings.Contains(codes(r), "tombstone_digest_mismatch") {
		t.Fatalf("digest mismatch not detected: %s", codes(r))
	}
	rs = append(good(t, 1), Rec{K: KFinal, T: 8000, ID: "never", State: StateDone})
	if r := Validate(rs, Expect{}); !strings.Contains(codes(r), "final_unknown") {
		t.Fatalf("final for unknown id not detected: %s", codes(r))
	}
}

func TestRejectedButProcessedAndUncertain(t *testing.T) {
	rs := good(t, 1)
	rs[0].Res = ResRejected
	if r := Validate(rs, Expect{}); !strings.Contains(codes(r), "rejected_but_processed") {
		t.Fatalf("rejected-but-processed not detected: %s", codes(r))
	}
	rs = good(t, 1)
	rs[0].Res = ResUncertain
	if r := Validate(rs, Expect{}); !r.Ok() || r.Uncertain != 1 {
		t.Fatalf("uncertain submit that was processed is allowed and counted: %s", codes(r))
	}
}

func TestSubmitRetriesAreAppended(t *testing.T) {
	rs := good(t, 1)
	first := Rec{K: KSubmit, T: 1, ID: "ta", Sha: rs[0].Sha, Len: rs[0].Len, Res: ResRejected, Err: "OOM", Try: 1}
	rs = append([]Rec{first}, rs...)
	r := Validate(rs, Expect{})
	if !r.Ok() || r.SubmitAttempts != 2 || r.UniqueSubmitted != 1 {
		t.Fatalf("retried submit must keep both attempts: %+v %s", r, codes(r))
	}
	rs[0].Sha = SHA256Hex([]byte("other"))
	if r := Validate(rs, Expect{}); !strings.Contains(codes(r), "submit_sha_conflict") {
		t.Fatalf("sha change between attempts not detected: %s", codes(r))
	}
}

func TestExpectedLostAndFailed(t *testing.T) {
	rs := good(t, 2)
	var out []Rec
	for _, r := range rs {
		if r.ID == "tb" && r.K != KSubmit {
			continue
		}
		out = append(out, r)
	}
	if r := Validate(out, Expect{ExpectedLost: []string{"tb"}}); !r.Ok() {
		t.Fatalf("declared-lost id must not fail: %s", r.Summary(5))
	}
	if r := Validate(out, Expect{}); r.Ok() {
		t.Fatal("undeclared loss must fail")
	}
}

// ---- 账本文件本身 ----

func TestWriterAppendOnlyAndStrictReader(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.jsonl")
	w, err := Create(p)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // 同一ID重复追加，不能覆盖
		if err := w.Append(Rec{K: KProcStart, ID: "x", Call: w.NextCall()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	rs, err := ReadFile(p)
	if err != nil || len(rs) != 3 {
		t.Fatalf("got %d recs err=%v", len(rs), err)
	}
	if rs[0].Call == rs[1].Call {
		t.Fatal("call ids must be unique")
	}
	if _, err := Create(p); err == nil {
		t.Fatal("Create must refuse to overwrite an existing ledger")
	}
	if err := w.Append(Rec{K: KEvent}); err == nil {
		t.Fatal("append after close must report an error")
	}
	if w.Err() == nil {
		t.Fatal("write error must be sticky")
	}

	trunc := filepath.Join(dir, "t.jsonl")
	if err := os.WriteFile(trunc, []byte(`{"k":"event","t":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(trunc); err == nil {
		t.Fatal("truncated last line must be an error")
	}
	bad := filepath.Join(dir, "b.jsonl")
	if err := os.WriteFile(bad, []byte("{\"k\":\"event\",\"t\":1,\"bogus\":1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(bad); err == nil {
		t.Fatal("unknown field must be an error")
	}
}

func TestPayloadRoundTrip(t *testing.T) {
	for _, n := range []int{64, 256, 4096} {
		p, err := MakePayload("abc-1", n)
		if err != nil || len(p) != n {
			t.Fatalf("n=%d err=%v len=%d", n, err, len(p))
		}
		if id, ok := VerifyPayload(p); !ok || id != "abc-1" {
			t.Fatalf("verify failed n=%d id=%s", n, id)
		}
		p[len(p)-1] ^= 1
		if _, ok := VerifyPayload(p); ok {
			t.Fatal("flipped byte must fail verification")
		}
	}
	if _, err := MakePayload("abc", 5); err == nil {
		t.Fatal("too small size must error")
	}
}

// ---- 空账本 / 必需检查 ----

func TestNegativeEmptyLedgerFails(t *testing.T) {
	for name, rs := range map[string][]Rec{
		"nil":            nil,
		"checks only":    {{K: KCheck, T: 1, Name: "x", Pass: Bool(true)}},
		"events only":    {{K: KEvent, T: 1, Name: "e"}},
		"cleared ledger": good(t, 3)[:0],
	} {
		r := Validate(rs, Expect{})
		if r.Ok() || !strings.Contains(codes(r), "empty_ledger") {
			t.Fatalf("%s: empty ledger must fail with empty_ledger, got ok=%v %s", name, r.Ok(), codes(r))
		}
	}
}

func TestNegativeMissingRequiredCheckCannotFalsePass(t *testing.T) {
	exp := Expect{RequiredChecks: []string{"per_id_reconcile_all"}}
	if r := Validate(good(t, 2), exp); !strings.Contains(codes(r), "missing_required_check") {
		t.Fatalf("absent required check must fail: %s", codes(r))
	}
	rs := append(good(t, 2), Rec{K: KCheck, T: 9000, Name: "per_id_reconcile_all", Pass: Bool(true)})
	if r := Validate(rs, exp); !r.Ok() {
		t.Fatalf("present+passed required check must pass: %s", r.Summary(5))
	}
	// 记录了但失败 / 只有错误：不能算“出现并通过”
	rs = append(good(t, 2), Rec{K: KCheck, T: 9000, Name: "per_id_reconcile_all", Pass: Bool(false)})
	if r := Validate(rs, exp); !strings.Contains(codes(r), "missing_required_check") || !strings.Contains(codes(r), "check_failed") {
		t.Fatalf("failed required check: %s", codes(r))
	}
	rs = append(good(t, 2), Rec{K: KCheck, T: 9000, Name: "per_id_reconcile_all", Err: "timeout"})
	if r := Validate(rs, exp); !strings.Contains(codes(r), "missing_required_check") || !strings.Contains(codes(r), "query_error") {
		t.Fatalf("errored required check: %s", codes(r))
	}
}

// ---- 脚本化调用次数（重试/panic/重放/到期重提交） ----

// extraCall 给 id 追加一次完整调用（start+end）
func extraCall(rs []Rec, id string, n int) []Rec {
	var st Rec
	for _, r := range rs {
		if r.K == KProcStart && r.ID == id {
			st = r
			break
		}
	}
	for i := 0; i < n; i++ {
		c := st
		c.T, c.Call = 8000+int64(2*i), "2:"+id+string(rune('a'+i))
		rs = append(rs, c, Rec{K: KProcEnd, T: c.T + 1, ID: id, Pid: 2, Call: c.Call, Ret: "nil"})
	}
	return rs
}

func TestExpectCallsAllowsScriptedRepeatsOnly(t *testing.T) {
	rs := extraCall(good(t, 3), "ta", 2) // ta 共 3 次调用
	r := Validate(rs, Expect{ExpectCalls: map[string]int{"ta": 3}})
	if !r.Ok() {
		t.Fatalf("scripted repeats must pass: %s", r.Summary(5))
	}
	if r.Duplicates != 2 || r.ScriptedRepeats != 2 {
		t.Fatalf("stats: %+v", r)
	}
	// 不在脚本表内的ID重复，仍然是未预期重复
	rs = extraCall(rs, "tb", 1)
	if r := Validate(rs, Expect{ExpectCalls: map[string]int{"ta": 3}}); !strings.Contains(codes(r), "unexpected_duplicate") {
		t.Fatalf("unscripted duplicate must fail: %s", codes(r))
	}
}

func TestNegativeExpectCallsMismatch(t *testing.T) {
	rs := extraCall(good(t, 2), "ta", 1) // ta 2 次
	for name, want := range map[string]int{"fewer": 3, "more": 1} {
		if r := Validate(rs, Expect{ExpectCalls: map[string]int{"ta": want}}); !strings.Contains(codes(r), "call_count_mismatch") {
			t.Fatalf("%s: call count mismatch not detected: %s", name, codes(r))
		}
	}
	// 期望表里的ID从未提交：不能静默忽略
	if r := Validate(good(t, 2), Expect{ExpectCalls: map[string]int{"ghost": 2}}); !strings.Contains(codes(r), "expect_calls_unknown_id") {
		t.Fatalf("unknown scripted id not detected: %s", codes(r))
	}
}
