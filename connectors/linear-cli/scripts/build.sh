#!/usr/bin/env bash
exec "$(dirname "$0")/../../../scripts/go-build.sh" linear-cli "$@"
