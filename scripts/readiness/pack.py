#!/usr/bin/env python3
"""把候选仓库打包成 Go module zip，并布置成 file:// module proxy 目录。

用法: python3 -I pack.py <repo_root> <proxy_dir> <version> <manifest_out>

- 包含: 仓库内所有 *.go / go.mod / go.sum（含未跟踪源码与测试）。
  只在**仓库根目录**排除 .git .pi docs scripts examples vendor node_modules 以及根下其它点开头目录；
  更深层的同名目录（例如未来的 pkg/docs、pkg/examples）一律入包，不会被悄悄漏掉。
  遇到嵌套 go.mod（会被 Go 当作独立模块、不进 zip）直接报错，不静默。README/LICENSE 不入包。
- 不使用 replace、不使用仓库路径冒充安装：下游只通过 GOPROXY=file://<proxy_dir> 解析该版本。
- 输出 <manifest_out>: 每个入包文件的 SHA256 以及清单自身摘要由调用方计算。
"""
import hashlib, os, sys, zipfile, json, datetime

MODULE = "github.com/stones-hub/taurus-pro-storage"
ROOT_EXCLUDE_DIRS = {".git", ".pi", "docs", "scripts", "examples", "vendor", "node_modules"}

def listing(root):
    files = []
    for dp, dn, fn in os.walk(root):
        at_root = os.path.abspath(dp) == os.path.abspath(root)
        if at_root:
            dn[:] = sorted(d for d in dn if d not in ROOT_EXCLUDE_DIRS and not d.startswith("."))
        else:
            dn[:] = sorted(dn)
            if "go.mod" in fn:
                raise SystemExit("pack: nested go.mod at %s would be a separate module and be dropped from the zip" % os.path.relpath(dp, root))
        for f in sorted(fn):
            if f.endswith(".go") or (at_root and f in ("go.mod", "go.sum")):
                files.append(os.path.relpath(os.path.join(dp, f), root))
    files.sort()
    return files

def main():
    if sys.argv[1] == "hash":  # 只重算清单，不打包：python3 -I pack.py hash <root> <out>
        root, out = sys.argv[2:4]
        lines = ["%s  %s" % (hashlib.sha256(open(os.path.join(root, r), "rb").read()).hexdigest(), r) for r in listing(root)]
        open(out, "w").write("\n".join(lines) + "\n")
        return
    root, proxy, version, manifest = sys.argv[1:5]
    files = listing(root)
    vdir = os.path.join(proxy, MODULE, "@v")
    os.makedirs(vdir, exist_ok=False)
    lines = []
    with zipfile.ZipFile(os.path.join(vdir, version + ".zip"), "w", zipfile.ZIP_DEFLATED) as z:
        for rel in files:
            data = open(os.path.join(root, rel), "rb").read()
            lines.append("%s  %s" % (hashlib.sha256(data).hexdigest(), rel))
            zi = zipfile.ZipInfo("%s@%s/%s" % (MODULE, version, rel), date_time=(1980, 1, 1, 0, 0, 0))
            zi.compress_type = zipfile.ZIP_DEFLATED
            zi.external_attr = 0o644 << 16
            z.writestr(zi, data)
    with open(os.path.join(root, "go.mod"), "rb") as f:
        gomod = f.read()
    open(os.path.join(vdir, version + ".mod"), "wb").write(gomod)
    t = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    open(os.path.join(vdir, version + ".info"), "w").write(json.dumps({"Version": version, "Time": t}))
    open(os.path.join(vdir, "list"), "w").write(version + "\n")
    open(manifest, "w").write("\n".join(lines) + "\n")
    print("packed %d files" % len(files))

if __name__ == "__main__":
    main()
