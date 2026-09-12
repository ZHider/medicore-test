#!/usr/bin/env bash
#
# 对全部 fuzz 目标各跑一段时间。
#
# 为什么需要脚本：`go test -fuzz` 一次只接受**一个**目标，
# 而目标分散在多个包里（security / jwt / service / model / audit / mask）。
# 手工敲一遍既容易漏，也没人愿意在每次改动后做。
#
# 用法：
#   bash backend/scripts/fuzz.sh          # 每个目标 30 秒
#   bash backend/scripts/fuzz.sh 5m       # 每个目标 5 分钟
#
# 退出码非 0 表示发现了会失败的输入，失败用例会被自动写入
# <pkg>/testdata/fuzz/<Target>/ 并**必须提交进仓库** —— 那是回归语料。

set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

FUZZTIME="${1:-30s}"
FAILED=0

# 从源码里发现目标，而不是手工维护一份列表：
# 新增 fuzz 目标时忘了加进列表，等于白写。
targets=$(grep -rhoE '^func (Fuzz[A-Za-z0-9_]+)' --include='*_test.go' ./internal \
  | awk '{print $2}' | sort -u)

if [ -z "$targets" ]; then
  echo "未发现任何 fuzz 目标"
  exit 1
fi

echo "发现 $(( $(echo "$targets" | wc -l) )) 个 fuzz 目标，每个运行 $FUZZTIME"
echo

for target in $targets; do
  # 定位目标所在的包
  dir=$(grep -rlE "^func ${target}\(" --include='*_test.go' ./internal | head -1 | xargs dirname 2>/dev/null)
  if [ -z "$dir" ]; then
    echo "!! 无法定位 $target 所在包，跳过"
    continue
  fi

  printf '%-38s ' "$target"
  if out=$(go test -run '^$' -fuzz "^${target}\$" -fuzztime "$FUZZTIME" "./${dir#./}/" 2>&1); then
    echo "ok"
  else
    echo "FAIL"
    echo "$out" | grep -E 'Failing input|panic|testdata/fuzz' | head -5 | sed 's/^/    /'
    FAILED=1
  fi
done

echo
if [ "$FAILED" -ne 0 ]; then
  echo "有目标失败。检查上面的 Failing input，并把它作为回归语料提交进仓库。"
  exit 1
fi
echo "全部 fuzz 目标通过（$FUZZTIME/目标）"
