#!/bin/sh
# Print the next release tag (vX.Y.Z) from the Conventional Commits since the
# last release tag, or nothing when no commit warrants a release.
#
#   scripts/next-version.sh            # bump from commit messages
#   scripts/next-version.sh minor      # force at least this bump (patch|minor|major)
#
# Commit prefix -> bump:
#   feat!: / fix(scope)!: / "BREAKING CHANGE:" footer   major (minor while < 1.0.0)
#   feat:                                               minor
#   fix: perf: revert:                                  patch
#   docs: test: chore: ci: build: refactor: style:      no release
#
# The first release (no tag yet) is v0.1.0.
set -eu

force="${1:-}"
case "$force" in "" | patch | minor | major) ;; *) echo "usage: $0 [patch|minor|major]" >&2; exit 2 ;; esac

last=$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' --exclude 'v*-*' 2>/dev/null || true)
if [ -n "$last" ]; then
  range="$last..HEAD"
  ver="${last#v}"
else
  range="HEAD"
  ver="0.0.0"
fi
major="${ver%%.*}"; rest="${ver#*.}"
minor="${rest%%.*}"; patch="${rest#*.}"

# 0 none, 1 patch, 2 minor, 3 major. %x1e separates commits; subject then body.
bump=$(git log --format='%s%n%b%x1e' "$range" | awk '
  BEGIN { RS = "\036"; b = 0 }
  {
    sub(/^\n+/, "")
    n = index($0, "\n"); subj = n ? substr($0, 1, n - 1) : $0
    if (subj ~ /^[a-zA-Z]+(\([^)]*\))?!:/) { b = 3; next }
    k = split($0, line, "\n")
    for (i = 2; i <= k; i++) if (line[i] ~ /^BREAKING[ -]CHANGE: /) b = 3
    if (b == 3) next
    if (subj ~ /^feat(\([^)]*\))?:/ && b < 2) b = 2
    else if (subj ~ /^(fix|perf|revert)(\([^)]*\))?:/ && b < 1) b = 1
  }
  END { print b }')

case "$force" in patch) [ "$bump" -ge 1 ] || bump=1 ;; minor) [ "$bump" -ge 2 ] || bump=2 ;; major) bump=3 ;; esac
# Before 1.0.0 a breaking change bumps minor; going to 1.0.0 is a deliberate
# `major` forced bump.
if [ "$bump" -eq 3 ] && [ "$major" -eq 0 ] && [ "$force" != major ]; then bump=2; fi

[ "$bump" -gt 0 ] || exit 0
if [ -z "$last" ]; then
  [ "$bump" -eq 3 ] && echo v1.0.0 || echo v0.1.0
  exit 0
fi
case "$bump" in
  1) patch=$((patch + 1)) ;;
  2) minor=$((minor + 1)); patch=0 ;;
  3) major=$((major + 1)); minor=0; patch=0 ;;
esac
echo "v$major.$minor.$patch"
