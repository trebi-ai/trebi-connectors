# vnc-cli

A Go CLI that controls a remote computer over VNC (RFB). It captures the screen, types, clicks, and watches the screen for changes. It is a Go port of [vncdotool](https://github.com/sibson/vncdotool) (MIT, see `NOTICE`). Its `serve` command is the Trebi adapter. The catalog entry `catalog/vnc` uses it.

The package `vnc` is a public RFB client. Perception imports it as `github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc`. The package `vnc/vnctest` is a fake RFB server for tests.

## Build

```bash
go build -o vnc-cli .
./scripts/build.sh            # writes ./build/vnc-cli with version metadata
```

## Use on its own

The commands come in a later step.
