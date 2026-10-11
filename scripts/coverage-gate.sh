#!/usr/bin/env bash
# Fail when test coverage drops below the floors: per package (packages with
# fewer than MIN_STMTS statements are skipped: a tiny main is not worth gating)
# and overall. Reads a Go cover profile.
#
#   go test -coverprofile=cover.out -coverpkg=./internal/...,./cmd/... ./... && scripts/coverage-gate.sh cover.out
#
# MIN_PKG (default 60) and MIN_TOTAL (default 75) are percentages.
set -euo pipefail
profile="${1:?cover profile}"
MIN_PKG="${MIN_PKG:-60}"
MIN_TOTAL="${MIN_TOTAL:-75}"
MIN_STMTS="${MIN_STMTS:-50}"

# profile lines: path/file.go:L.C,L.C statements count
awk -v minpkg="$MIN_PKG" -v mintotal="$MIN_TOTAL" -v minstmts="$MIN_STMTS" '
  NR == 1 { next }                      # "mode: set"
  {
    # with -coverpkg every test binary reports every block, so the same block
    # appears once per package tested: keep one copy, covered if any run hit it
    blk[$1] = $2
    if ($3 > 0) hit[$1] = 1
  }
  END {
    for (b in blk) {
      split(b, a, ":"); file = a[1]
      n = split(file, parts, "/"); pkg = ""
      for (i = 1; i < n; i++) pkg = pkg (i > 1 ? "/" : "") parts[i]
      stmts[pkg] += blk[b]; total += blk[b]
      if (hit[b]) { covered[pkg] += blk[b]; ctotal += blk[b] }
    }
    bad = 0
    for (p in stmts) {
      if (stmts[p] < minstmts) continue
      pct = 100 * covered[p] / stmts[p]
      mark = pct < minpkg ? "  <-- below " minpkg "%" : ""
      if (pct < minpkg) bad = 1
      printf "%6.1f%%  %s%s\n", pct, p, mark
    }
    tp = 100 * ctotal / total
    printf "%6.1f%%  total (floor %s%%)\n", tp, mintotal
    if (tp < mintotal) bad = 1
    exit bad
  }' "$profile" | sort -k2
