#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
exec ./build.sh windows amd64
