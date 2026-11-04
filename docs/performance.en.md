# Performance

[← Back to README](../README.en.md) · [简体中文](performance.md) | English

Measured with the bundled load generator `cmd/vivant-bench`: everyone in the same room, 25 people each posting every 2 seconds (about 12.5 messages a second for the room), the server limited to **1 CPU core** with `GOMEMLIMIT=600MiB`:

| Sessions online | Mode | Delivered | Latency p50 / p99 | Memory |
| --- | --- | --- | --- | --- |
| 1000 | full interface | 100% | 14 ms / 37 ms | 157 MB |
| 2000 | full interface | 100% | 30 ms / 148 ms | 355 MB |
| 3000 | full interface | 100% | 91 ms / 478 ms | 532 MB |
| 4000 | line mode | 100% | 423 ms / 1.4 s (core saturated) | 360 MB |

Bottom line: **a 1-core, 1 GB VPS handles about 2000 people online at once in the full interface**; at 3000 everything is still delivered but latency starts to climb. For more people, add CPU cores (Go uses them all). The same number of people spread over several rooms costs far less.

Key design choices:

- **A small hand-written terminal renderer.** It uses the terminal's scroll region, so each new message sends only its new lines, and the fixed lines are redrawn only when they change. An early version on a general TUI framework re-laid out the whole screen every frame: 300 people online took 883 MB and latency reached seconds.
- **Rendered lines are shared between sessions.** A message looks the same to most people, so it's formatted once. Only your own messages and ones that mention you are rendered separately.
- **Scrollback is rendered on demand.** Each session keeps pointers to shared messages and renders them only when you scroll.
- **No general styling engine on the hot path.** Colours are precomputed escape codes and the width of ASCII text is computed directly.
- **Ordinary chat messages don't refresh the status bar**, so they don't contend for the global lock.

Try it yourself: `make bench`, or see the comment at the top of `cmd/vivant-bench/main.go`.
