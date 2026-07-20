# Post-processing examples

vole can pipe each raw whisper transcript through an external **post-processing
script** and use the script's output as the injected text. This directory holds
ready-made scripts you can copy and point `postprocess` at in your
`~/.config/vole/config.yaml` (see the top-level `config.yaml.example`).

## Hook contract

A post-processing script is any executable that:

- reads the **raw transcript** on **stdin**,
- writes the **improved transcript** to **stdout** (a single trailing newline is
  trimmed by vole),
- uses **stderr** for diagnostics only (never the transcript content — stderr is
  not shown to the user, but keep secrets out of it),
- needs a **shebang** (`#!/bin/sh`, `#!/usr/bin/env python3`, …) and the
  **execute bit** (`chmod +x`). vole runs the path directly, not via a shell.

Behaviour on failure is safe by design: if the script exits non-zero, times out
(see `postprocess_timeout`, default 30 s), or returns empty/whitespace-only
output, vole falls back to the **raw transcript** — a broken script never blocks
dictation.

With post-processing on, the improved text is pasted into the focused window and
**both** the raw and the improved text land on the clipboard (raw as the previous
Klipper entry) and in the dictation history (improved as the newest entry). While
the script runs, the tray dot turns **blue** so you can see the hook is active
even though recognition has finished.

Toggle the hook at runtime via the tray **Post-process** checkbox (shown only
when a `postprocess` script is configured); it starts from `postprocess_on` on
each daemon restart.

## Examples

- `ollama-qwen-cloud-vision-repair.py` — repairs the transcript with a cloud
  vision LLM reached through the local `ollama` client (`qwen3.5:cloud`). It
  feeds the model the raw transcript, a screenshot of the **active window**
  (captured with `spectacle`, downscaled with `ffmpeg`), and the last few vole
  dictations from `history.jsonl` for vocabulary/topic context. Useful for short
  phrases that mishear easily. Env knobs: `VOLE_PP_MODEL`, `VOLE_OLLAMA`,
  `VOLE_PP_HISTORY`, `VOLE_PP_NO_SCREEN`, `VOLE_PP_IMG_MAX`,
  `VOLE_PP_OLLAMA_TIMEOUT`.

  **Privacy:** the screenshot of the active window and the transcript are sent to
  the ollama cloud model. Nothing is logged to stdout; stderr gets only short
  diagnostics. Review the model/provider before enabling.

## Writing your own

Copy an example, or start fresh:

```sh
#!/bin/sh
# uppercase marker — a visible smoke test
tr '[:lower:]' '[:upper:]'
```

Then:

```yaml
postprocess: ~/.config/vole/postprocess.sh
postprocess_on: true
postprocess_timeout: 30   # generous for cloud/vision scripts; 15 for local ones
```

`chmod +x` the script and restart vole (`systemctl --user restart vole`). Keep
scripts fast and deterministic; long/blocking work eats the dictation latency
(up to `postprocess_timeout`, then raw fallback).