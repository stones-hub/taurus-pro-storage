package dockerx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

const fullID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type fake struct {
	calls   [][]string
	labels  map[string]string
	id      string
	missing bool
}

func (f *fake) Run(ctx context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if args[0] == "inspect" {
		if f.missing {
			return "", errors.New("Error: No such container")
		}
		b, _ := json.Marshal([]map[string]any{{"Id": f.id, "Name": "/x", "Config": map[string]any{"Labels": f.labels}, "State": map[string]any{"Running": true}}})
		return string(b), nil
	}
	if len(args) > 1 && args[0] == "volume" && args[1] == "inspect" {
		b, _ := json.Marshal([]map[string]any{{"Name": "vol1", "Labels": f.labels}})
		return string(b), nil
	}
	return "", nil
}

// mutating 返回除 inspect 外发出的命令
func (f *fake) mutating() [][]string {
	var out [][]string
	for _, c := range f.calls {
		if c[0] == "inspect" || (c[0] == "volume" && c[1] == "inspect") {
			continue
		}
		out = append(out, c)
	}
	return out
}

func newDocker(t *testing.T, f *fake) (*Docker, Res) {
	t.Helper()
	m, err := NewManifest(filepath.Join(t.TempDir(), "m.json"))
	if err != nil {
		t.Fatal(err)
	}
	d := &Docker{R: f, Owner: "me", Run: "r1", Manifest: m}
	r := Res{Kind: "container", ID: fullID, Name: "c", ExpectOwner: "me", ExpectRun: "r1"}
	if err := m.Add(r); err != nil {
		t.Fatal(err)
	}
	return d, r
}

func TestOwnedResourceIsOperated(t *testing.T) {
	f := &fake{id: fullID, labels: map[string]string{LabelOwner: "me", LabelRun: "r1"}}
	d, r := newDocker(t, f)
	ctx := context.Background()
	if err := d.Kill(ctx, r, "KILL"); err != nil {
		t.Fatal(err)
	}
	if got := f.mutating(); len(got) != 1 || strings.Join(got[0], " ") != "kill --signal KILL "+fullID {
		t.Fatalf("unexpected commands %v", got)
	}
}

func TestMismatchedLabelsAreRefusedWithoutTouching(t *testing.T) {
	for name, lab := range map[string]map[string]string{
		"other owner": {LabelOwner: "someone-else", LabelRun: "r1"},
		"other run":   {LabelOwner: "me", LabelRun: "r2"},
		"no labels":   {},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fake{id: fullID, labels: lab}
			d, r := newDocker(t, f)
			ctx := context.Background()
			ops := map[string]func() error{
				"kill":   func() error { return d.Kill(ctx, r, "KILL") },
				"start":  func() error { return d.Start(ctx, r) },
				"exec":   func() error { _, err := d.Exec(ctx, r, "redis-cli", "ping"); return err },
				"cp":     func() error { return d.CopyFrom(ctx, r, "/data/dump.rdb", "/tmp/x") },
				"remove": func() error { return d.Remove(ctx, r) },
			}
			for op, fn := range ops {
				if err := fn(); !errors.Is(err, ErrNotOwned) {
					t.Fatalf("%s: want ErrNotOwned, got %v", op, err)
				}
			}
			if m := f.mutating(); len(m) != 0 {
				t.Fatalf("object was touched despite mismatch: %v", m)
			}
			if len(d.Manifest.Snapshot()) != 1 {
				t.Fatal("refused remove must not drop the manifest entry")
			}
		})
	}
}

func TestIDMismatchAndShortIDRefused(t *testing.T) {
	f := &fake{id: strings.Repeat("f", 64), labels: map[string]string{LabelOwner: "me", LabelRun: "r1"}}
	d, r := newDocker(t, f)
	if err := d.Kill(context.Background(), r, "KILL"); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("id mismatch must be refused: %v", err)
	}
	short := r
	short.ID = fullID[:12]
	if err := d.Kill(context.Background(), short, "KILL"); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("short id must be refused: %v", err)
	}
	if len(f.mutating()) != 0 {
		t.Fatal("touched")
	}
}

func TestCleanupAllOnlyListedAndRefusesForeign(t *testing.T) {
	f := &fake{id: fullID, labels: map[string]string{LabelOwner: "foreign", LabelRun: "r1"}}
	d, _ := newDocker(t, f)
	log, err := d.CleanupAll(context.Background())
	if !errors.Is(err, ErrNotOwned) {
		t.Fatalf("cleanup of foreign-labelled resource must fail: %v (%v)", err, log)
	}
	if len(f.mutating()) != 0 {
		t.Fatal("foreign object removed")
	}
	f2 := &fake{id: fullID, labels: map[string]string{LabelOwner: "me", LabelRun: "r1"}}
	d2, _ := newDocker(t, f2)
	if _, err := d2.CleanupAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := f2.mutating()
	if len(m) != 1 || strings.Join(m[0], " ") != "rm -f "+fullID {
		t.Fatalf("expected exact-id rm, got %v", m)
	}
	if len(d2.Manifest.Snapshot()) != 0 {
		t.Fatal("manifest not emptied")
	}
}

func TestGoneResourceIsNotAnError(t *testing.T) {
	f := &fake{missing: true}
	d, r := newDocker(t, f)
	if err := d.Remove(context.Background(), r); err != nil {
		t.Fatalf("already-removed resource: %v", err)
	}
}

func TestExecRunnerForbidsPrune(t *testing.T) {
	_, err := ExecRunner{Bin: "/nonexistent-docker"}.Run(context.Background(), "system", "prune", "-f")
	if err == nil || !strings.Contains(err.Error(), "prune is forbidden") {
		t.Fatalf("prune must be rejected before exec: %v", err)
	}
	_, err = ExecRunner{Bin: "/nonexistent-docker"}.Run(context.Background(), "volume", "prune")
	if err == nil || !strings.Contains(err.Error(), "prune is forbidden") {
		t.Fatal(err)
	}
}

func TestVolumeVerify(t *testing.T) {
	f := &fake{labels: map[string]string{LabelOwner: "other", LabelRun: "r1"}}
	d, _ := newDocker(t, f)
	v := Res{Kind: "volume", ID: "vol1", Name: "vol1", ExpectOwner: "me", ExpectRun: "r1"}
	if err := d.Remove(context.Background(), v); !errors.Is(err, ErrNotOwned) {
		t.Fatal(fmt.Sprint("volume with foreign labels must be refused: ", err))
	}
	if len(f.mutating()) != 0 {
		t.Fatal("touched volume")
	}
}

// ---- 归属：Res 期望必须等于当前 Docker 上下文 ----

func TestResExpectationMustEqualCurrentContext(t *testing.T) {
	// 容器的真实标签与 Res 登记完全一致（都是 decoy owner），但当前上下文是 me/r1：不得操作
	for name, tc := range map[string]struct {
		res    Res
		labels map[string]string
	}{
		"manifest entry of another owner": {Res{Kind: "container", ID: fullID, Name: "c", ExpectOwner: "decoy", ExpectRun: "r1"}, map[string]string{LabelOwner: "decoy", LabelRun: "r1"}},
		"manifest entry of another run":   {Res{Kind: "container", ID: fullID, Name: "c", ExpectOwner: "me", ExpectRun: "r2"}, map[string]string{LabelOwner: "me", LabelRun: "r2"}},
		"empty expectation":               {Res{Kind: "container", ID: fullID, Name: "c"}, map[string]string{}},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fake{id: fullID, labels: tc.labels}
			d, _ := newDocker(t, f)
			ctx := context.Background()
			for op, fn := range map[string]func() error{
				"kill":   func() error { return d.Kill(ctx, tc.res, "KILL") },
				"start":  func() error { return d.Start(ctx, tc.res) },
				"exec":   func() error { _, err := d.Exec(ctx, tc.res, "x"); return err },
				"cp":     func() error { return d.CopyTo(ctx, tc.res, "/a", "/b") },
				"remove": func() error { return d.Remove(ctx, tc.res) },
			} {
				if err := fn(); !errors.Is(err, ErrNotOwned) {
					t.Fatalf("%s: want ErrNotOwned, got %v", op, err)
				}
			}
			if m := f.mutating(); len(m) != 0 {
				t.Fatalf("touched: %v", m)
			}
		})
	}
}

func TestCleanupRequiresMatchingRunAndExplicitDecoyContext(t *testing.T) {
	labels := map[string]string{LabelOwner: "me-decoy", LabelRun: "r1"}
	res := Res{Kind: "container", ID: fullID, Name: "decoy", ExpectOwner: "me-decoy", ExpectRun: "r1"}
	mk := func(owner, run string) (*Docker, *fake) {
		f := &fake{id: fullID, labels: labels}
		m, err := NewManifest(filepath.Join(t.TempDir(), "m.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Add(res); err != nil {
			t.Fatal(err)
		}
		return &Docker{R: f, Owner: owner, Run: run, Manifest: m}, f
	}
	for name, c := range map[string][2]string{"main owner": {"me", "r1"}, "wrong run": {"me-decoy", "r2"}} {
		d, f := mk(c[0], c[1])
		if _, err := d.CleanupAll(context.Background()); !errors.Is(err, ErrNotOwned) {
			t.Fatalf("%s: cleanup must be refused, got %v", name, err)
		}
		if len(f.mutating()) != 0 || len(d.Manifest.Snapshot()) != 1 {
			t.Fatalf("%s: refused cleanup touched something: %v", name, f.mutating())
		}
	}
	d, f := mk("me-decoy", "r1")
	if _, err := d.CleanupAll(context.Background()); err != nil {
		t.Fatalf("explicit decoy owner + same run must clean: %v", err)
	}
	if m := f.mutating(); len(m) != 1 || strings.Join(m[0], " ") != "rm -f "+fullID {
		t.Fatalf("want one exact rm, got %v", m)
	}
}

// ---- 创建流程：create -> 登记完整ID -> start ----

type script struct {
	calls     [][]string
	createOut string
	createErr error
	startErr  error
	inspect   string // inspect 命令的输出；空表示 No such
	rmErr     error
}

func (s *script) Run(ctx context.Context, args ...string) (string, error) {
	s.calls = append(s.calls, args)
	switch args[0] {
	case "create":
		return s.createOut, s.createErr
	case "start":
		return "", s.startErr
	case "inspect":
		if s.inspect == "" {
			return "", errors.New("Error: No such container")
		}
		return s.inspect, nil
	case "rm":
		return "", s.rmErr
	}
	return "", nil
}

func (s *script) verbs() []string {
	var v []string
	for _, c := range s.calls {
		if c[0] != "inspect" {
			v = append(v, c[0])
		}
	}
	return v
}

func inspectJSON(id string, labels map[string]string) string {
	b, _ := json.Marshal([]map[string]any{{"Id": id, "Name": "/x", "Config": map[string]any{"Labels": labels}, "State": map[string]any{}}})
	return string(b)
}

func TestStartFailureKeepsRegisteredContainerForCleanup(t *testing.T) {
	s := &script{createOut: fullID + "\n", startErr: errors.New("port is already allocated")}
	s.inspect = inspectJSON(fullID, map[string]string{LabelOwner: "me", LabelRun: "r1"})
	m, _ := NewManifest(filepath.Join(t.TempDir(), "m.json"))
	d := &Docker{R: s, Owner: "me", Run: "r1", Manifest: m}
	r, err := d.CreateContainer(context.Background(), "c1", "p", "img")
	if err != nil {
		t.Fatal(err)
	}
	if s.calls[0][0] != "create" {
		t.Fatalf("must use create, not run: %v", s.calls[0])
	}
	if err := d.Start(context.Background(), r); err == nil {
		t.Fatal("start error must surface")
	}
	if got := m.Snapshot(); len(got) != 1 || got[0].ID != fullID {
		t.Fatalf("container must remain in manifest after start failure: %+v", got)
	}
	if _, err := d.CleanupAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := strings.Join(s.verbs(), ","); v != "create,start,rm" {
		t.Fatalf("unexpected command sequence %s", v)
	}
	if last := s.calls[len(s.calls)-1]; strings.Join(last, " ") != "rm -f "+fullID {
		t.Fatalf("cleanup must remove by exact full id: %v", last)
	}
}

func TestRegisterFailureRemovesExactCreatedContainer(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManifest(filepath.Join(dir, "m.json"))
	m.Path = filepath.Join(dir, "no-such-dir", "m.json") // 之后任何落盘都会失败
	s := &script{createOut: fullID + "\n"}
	s.inspect = inspectJSON(fullID, map[string]string{LabelOwner: "me", LabelRun: "r1"})
	d := &Docker{R: s, Owner: "me", Run: "r1", Manifest: m}
	_, err := d.CreateContainer(context.Background(), "c1", "p", "img")
	if err == nil || !strings.Contains(err.Error(), "removed by exact id") {
		t.Fatalf("want register failure with exact removal, got %v", err)
	}
	if last := s.calls[len(s.calls)-1]; strings.Join(last, " ") != "rm -f "+fullID {
		t.Fatalf("remedy must be rm -f <full id>: %v", last)
	}
}

func TestCreateErrorAdoptsOnlyOwnedContainer(t *testing.T) {
	mk := func(labels map[string]string) (*script, *Docker) {
		s := &script{createErr: errors.New("boom")}
		s.inspect = inspectJSON(fullID, labels)
		m, _ := NewManifest(filepath.Join(t.TempDir(), "m.json"))
		return s, &Docker{R: s, Owner: "me", Run: "r1", Manifest: m}
	}
	s, d := mk(map[string]string{LabelOwner: "me", LabelRun: "r1"})
	r, err := d.CreateContainer(context.Background(), "c1", "p", "img")
	if err == nil || r.ID != fullID || len(d.Manifest.Snapshot()) != 1 {
		t.Fatalf("owned container must be registered for cleanup: r=%+v err=%v", r, err)
	}
	s, d = mk(map[string]string{LabelOwner: "someone", LabelRun: "r1"})
	r, err = d.CreateContainer(context.Background(), "c1", "p", "img")
	if err == nil || r.ID != "" || len(d.Manifest.Snapshot()) != 0 || len(s.mutatingVerbs()) != 1 {
		t.Fatalf("foreign container must not be adopted or touched: r=%+v err=%v calls=%v", r, err, s.calls)
	}
}

func (s *script) mutatingVerbs() []string { return s.verbs() }

func TestGoneVolumeMessageIsRecognised(t *testing.T) {
	for _, msg := range []string{"Error response from daemon: get v: no such volume", "Error: No such container: x"} {
		if !isNoSuch(errors.New(msg)) {
			t.Fatalf("%q must be recognised as gone", msg)
		}
	}
	if isNoSuch(errors.New("Cannot connect to the Docker daemon")) {
		t.Fatal("daemon failure must not be treated as gone")
	}
}

// ctxScript 在 ctx 已取消时拒绝执行（模拟 CommandContext 被取消），用来证明补救动作使用独立 ctx。
type ctxScript struct {
	script
	volInspect string
	createVol  error
	ctxLeft    int // 允许 run ctx 已取消仍成功的调用数（0 = 无）
}

func (c *ctxScript) Run(ctx context.Context, args ...string) (string, error) {
	if args[0] == "create" || (args[0] == "volume" && args[1] == "create") {
		c.calls = append(c.calls, args)
		if args[0] == "volume" {
			return "", c.createVol
		}
		return c.createOut, c.createErr
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if args[0] == "volume" && args[1] == "inspect" {
		c.calls = append(c.calls, args)
		if c.volInspect == "" {
			return "", errors.New("Error: no such volume")
		}
		return c.volInspect, nil
	}
	return c.script.Run(ctx, args...)
}

func canceledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestAdoptContainerSurvivesCanceledRunContext(t *testing.T) {
	c := &ctxScript{script: script{createErr: errors.New("signal: killed")}}
	c.inspect = inspectJSON(fullID, map[string]string{LabelOwner: "me", LabelRun: "r1"})
	m, _ := NewManifest(filepath.Join(t.TempDir(), "m.json"))
	d := &Docker{R: c, Owner: "me", Run: "r1", Manifest: m}
	r, err := d.CreateContainer(canceledCtx(), "c1", "p", "img")
	if err == nil || r.ID != fullID || len(m.Snapshot()) != 1 {
		t.Fatalf("canceled run ctx must not prevent exact adoption: r=%+v err=%v", r, err)
	}
	// 名字相同但完整ID/标签不符：仍不认领
	c2 := &ctxScript{script: script{createErr: errors.New("boom")}}
	c2.inspect = inspectJSON(fullID, map[string]string{LabelOwner: "me", LabelRun: "other"})
	m2, _ := NewManifest(filepath.Join(t.TempDir(), "m.json"))
	d2 := &Docker{R: c2, Owner: "me", Run: "r1", Manifest: m2}
	if r, _ := d2.CreateContainer(canceledCtx(), "c1", "p", "img"); r.ID != "" || len(m2.Snapshot()) != 0 {
		t.Fatalf("foreign run label must not be adopted: %+v", r)
	}
}

func TestRegisterFailureRemovalSurvivesCanceledRunContext(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManifest(filepath.Join(dir, "m.json"))
	m.Path = filepath.Join(dir, "no-such-dir", "m.json")
	c := &ctxScript{script: script{createOut: fullID + "\n"}}
	c.inspect = inspectJSON(fullID, map[string]string{LabelOwner: "me", LabelRun: "r1"})
	d := &Docker{R: c, Owner: "me", Run: "r1", Manifest: m}
	_, err := d.CreateContainer(canceledCtx(), "c1", "p", "img")
	if err == nil || !strings.Contains(err.Error(), "removed by exact id") {
		t.Fatalf("want exact removal despite canceled ctx, got %v", err)
	}
}

func volJSON(name string, labels map[string]string) string {
	b, _ := json.Marshal([]map[string]any{{"Name": name, "Labels": labels}})
	return string(b)
}

func TestCreateVolumeErrorAdoptsOnlyExactOwnedVolume(t *testing.T) {
	mk := func(name string, labels map[string]string) (*Docker, *Manifest) {
		c := &ctxScript{createVol: errors.New("signal: killed"), volInspect: volJSON(name, labels)}
		m, _ := NewManifest(filepath.Join(t.TempDir(), "m.json"))
		return &Docker{R: c, Owner: "me", Run: "r1", Manifest: m}, m
	}
	d, m := mk("v1", map[string]string{LabelOwner: "me", LabelRun: "r1"})
	if r, err := d.CreateVolume(canceledCtx(), "v1", "p"); err == nil || r.ID != "v1" || len(m.Snapshot()) != 1 {
		t.Fatalf("owned volume must be adopted: r=%+v err=%v", r, err)
	}
	d, m = mk("v1", map[string]string{LabelOwner: "me", LabelRun: "other"})
	if r, _ := d.CreateVolume(canceledCtx(), "v1", "p"); r.ID != "" || len(m.Snapshot()) != 0 {
		t.Fatalf("foreign-run volume must not be adopted: %+v", r)
	}
	d, m = mk("different", map[string]string{LabelOwner: "me", LabelRun: "r1"})
	if r, _ := d.CreateVolume(canceledCtx(), "v1", "p"); r.ID != "" || len(m.Snapshot()) != 0 {
		t.Fatalf("name mismatch must not be adopted: %+v", r)
	}
}
