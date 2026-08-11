#!/bin/sh
# Emits the -ldflags value embedding version info: githash(+dirty) - date.
# Usage: go build -ldflags "$(./scripts/ldflags.sh)" .
set -e
commit=$(git rev-parse --short HEAD 2>/dev/null || echo dev)
if [ -n "$(git status --porcelain 2>/dev/null)" ]; then
  commit="${commit}+dirty"
fi
date=$(date -u +%Y-%m-%d)
v=github.com/jclement/owgbot/internal/version
# -s -w strips the symbol table and DWARF debug info (~30% smaller binary);
# panic stack traces keep their function names regardless.
printf -- "-s -w -X %s.Commit=%s -X %s.Date=%s" "$v" "$commit" "$v" "$date"
if [ -n "$OWGBOT_TAG" ]; then
  printf -- " -X %s.Tag=%s" "$v" "$OWGBOT_TAG"
fi
