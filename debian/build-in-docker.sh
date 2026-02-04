#!/bin/sh
set -eu

PKG_NAME=composectl
OUT_DIR=/out
WORK_DIR=/work

mkdir -p "$WORK_DIR"
cp -a /src "$WORK_DIR/$PKG_NAME"
git config --global --add safe.directory "$WORK_DIR/$PKG_NAME"

cd "$WORK_DIR/$PKG_NAME"
debuild --no-lintian -b -us -uc

mkdir -p "$OUT_DIR"
mv "$WORK_DIR"/*.deb "$OUT_DIR/"
mv "$WORK_DIR"/*.changes "$OUT_DIR/" 2>/dev/null || true
mv "$WORK_DIR"/*.buildinfo "$OUT_DIR/" 2>/dev/null || true
