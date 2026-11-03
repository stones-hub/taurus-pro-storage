"""pack.py 的 listing 单元测试：python3 -I test_pack.py"""
import importlib.util, os, sys, tempfile, unittest

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("pack", os.path.join(HERE, "pack.py"))
pack = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pack)


def mk(root, rels):
    for r in rels:
        p = os.path.join(root, r)
        os.makedirs(os.path.dirname(p), exist_ok=True)
        open(p, "w").write("package x\n")


class ListingTest(unittest.TestCase):
    def test_root_excludes_only_at_root(self):
        with tempfile.TemporaryDirectory() as d:
            mk(d, ["go.mod", "go.sum", "a.go",
                   "docs/x.go", "scripts/x.go", "examples/x.go", "vendor/x.go", "node_modules/x.go", ".pi/x.go", ".git/x.go",
                   "pkg/docs/keep.go", "pkg/examples/keep.go", "pkg/scripts/keep.go", "pkg/vendor/keep.go", "pkg/q/node_modules/keep.go",
                   "pkg/q/testdata/keep.go"])
            got = pack.listing(d)
            self.assertEqual(got, sorted(["go.mod", "go.sum", "a.go",
                                          "pkg/docs/keep.go", "pkg/examples/keep.go", "pkg/scripts/keep.go", "pkg/vendor/keep.go",
                                          "pkg/q/node_modules/keep.go", "pkg/q/testdata/keep.go"]))

    def test_untracked_and_test_files_included_nested_gomod_rejected(self):
        with tempfile.TemporaryDirectory() as d:
            mk(d, ["go.mod", "pkg/a/new_untracked.go", "pkg/a/new_test.go"])
            self.assertEqual(pack.listing(d), ["go.mod", "pkg/a/new_test.go", "pkg/a/new_untracked.go"])
            mk(d, ["pkg/sub/go.mod"])
            with self.assertRaises(SystemExit):
                pack.listing(d)

    def test_nested_go_sum_not_packaged(self):
        with tempfile.TemporaryDirectory() as d:
            mk(d, ["go.mod", "pkg/go.sum"])
            self.assertEqual(pack.listing(d), ["go.mod"])


if __name__ == "__main__":
    unittest.main()
