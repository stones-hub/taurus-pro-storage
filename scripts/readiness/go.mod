module taurus-readiness

go 1.27.3

// 候选模块 github.com/stones-hub/taurus-pro-storage 的 require 行由 scripts/strict-readiness.sh
// 在临时工作副本里按本轮打包出的合成版本注入（不使用 replace）。本目录原样只能构建/测试
// ledger 与 dockerx 两个不依赖候选的包。
