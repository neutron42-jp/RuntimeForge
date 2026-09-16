# RuntimeForge

A local, LM Studio-like runtime manager for `llama.cpp`.

RuntimeForge builds `llama.cpp` runtimes from Git repositories (upstream **or any fork**),
installs them side by side, and **automatically selects the right runtime for each model**
based on the model's architecture and the GPU backends available on the host. It serves an
OpenAI-compatible API, so existing clients work unchanged.

> [!IMPORTANT]
> **This project was generated with AI assistance** (via [opencode](https://opencode.ai)).
> The code, spec and documentation are AI-authored and may contain mistakes, insecure
> defaults or unfinished parts. Review and test it yourself before relying on it.

## Why

LM Studio ships only the official upstream `llama.cpp` builds. Models whose architecture is
not yet in upstream (or only in a fork) simply cannot run. RuntimeForge lets you register any
fork by Git URL, build it locally with the backends you want, and have the correct runtime
chosen per model automatically — no manual runtime switching.

## Features

- **Git sources** — add upstream and/or forks (`name`, `url`, `ref`). Update *check*
  (`git ls-remote`) is separate from *build*.
- **Combined multi-backend builds** — CUDA + Vulkan + CPU are compiled together in a single
  CMake run (`-DGGML_CUDA=ON -DGGML_VULKAN=ON`); `llama.cpp` picks the device at runtime.
- **Automatic runtime selection** — after a build, supported architectures are extracted from
  `src/llama-arch.cpp`; a model is matched to the newest runtime that supports its architecture
  and an available backend (CUDA > Vulkan > CPU by default). Manual per-model / per-format
  overrides are also supported.
- **Model registry** — register folders (with a configurable recursion depth) and individual
  `.gguf` files; split shards are grouped; multimodal projectors are flagged and hidden by default.
- **Per-model load settings** — mirroring LM Studio: context size, GPU offload (`-ngl`),
  CPU threads (`-t`), eval batch size (`-b`), flash attention, KV-cache quantization
  (`--cache-type-k/-v`), **offload KV cache to GPU**, **model load mode** (`--load-mode`:
  auto/none/mmap/mlock/mmap+mlock), seed, RoPE frequency base/scale, CPU affinity, and a
  free-form **extra llama.cpp arguments** field passed to `llama-server` verbatim.
  Edit them per model from the Models tab (`Config…`, an in-app modal) or at load time on the
  Server tab; blank fields fall back to the saved value / global default. `Save as model default`
  persists them.
- **OpenAI-compatible API** — `/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`.
  Send `"model": "loaded"` to route to whatever model is currently loaded.
- **Live inference logs** — `llama-server` stdout/stderr is streamed to the UI.
- **Fully overridable configuration** — built-in defaults, `config.toml`, `config.d/*.toml`,
  and per-source / per-backend / per-model / per-request overrides (including build arguments).
- **Web UI + CLI + systemd user unit** (with optional socket activation).

## Requirements

- Linux (developed on Fedora; Ubuntu should work)
- Go 1.26+ and Node.js 20+ (Node only to build the web UI)
- Build tools: `cmake`, `ninja`, `git`, a C/C++ compiler
- Optional: CUDA toolkit (`nvcc`) for CUDA builds, `glslc` (shaderc) for Vulkan builds

## Build

```bash
make ui      # build the web UI into web/dist (needs npm)
make build   # build bin/runtimeforge (embeds web/dist)
```

## Run

```bash
./bin/runtimeforge serve --port 1234
# open http://127.0.0.1:1234
```

The OpenAI-compatible endpoint is `http://127.0.0.1:1234/v1`.

```bash
curl http://127.0.0.1:1234/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{"model":"loaded","messages":[{"role":"user","content":"hi"}]}'
```

## Install as a user service (autostart)

```bash
make install                              # build + install to ~/.local/bin + restart service if enabled
~/.local/bin/runtimeforge install-systemd # write ~/.config/systemd/user/runtimeforge.service
systemctl --user daemon-reload
systemctl --user enable --now runtimeforge
loginctl enable-linger "$USER"            # start at boot even without login
```

Manage it with `systemctl --user {status,restart,stop} runtimeforge` and
`journalctl --user -u runtimeforge -f`.

## CLI

```
runtimeforge serve|start|stop|status   daemon control
runtimeforge install-systemd           write the systemd --user unit
runtimeforge source list|add|remove|check
runtimeforge build <source> [--backend cuda] [--backend vulkan] [--backend cpu] [--commit SHA]
runtimeforge runtime list|remove
runtimeforge model scan|list
runtimeforge resolve <model>           show which runtime would be auto-selected
runtimeforge hardware | gguf <file> | arch <src> | paths | config
```

## HTTP API

- Management: `/api/v1/*` — system, config, sources, builds, runtimes, selections, models,
  instances, and a `/api/v1/events` SSE stream (build logs, inference logs, state changes).
- OpenAI-compatible: `/v1/models`, `/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`.

## Configuration

Configuration resolves in layers (later wins): built-in defaults → `config.toml` →
`config.d/*.toml` → per-source/per-backend/per-model/per-request overrides. Everything is
overridable, including CMake defines, extra configure/build args, and the compiler.

All state lives under `~/.runtimeforge/` (override with `RUNTIMEFORGE_HOME`):

```
~/.runtimeforge/
├── config.toml
├── config.d/
├── sources/        # git checkouts
├── runtimes/       # built runtime variants (self-contained)
├── models/         # default model scan dir
├── state/          # registries and selections
├── logs/
└── cache/          # build trees
```

See [SPEC.md](SPEC.md) for the full design.

## License

Released into the public domain under the [Unlicense](LICENSE) — use it however you like.
Bundled third-party components (React, BurntSushi/toml) keep their own licenses; see
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## Status

Early development, Linux-only.
