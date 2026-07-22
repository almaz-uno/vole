#!/usr/bin/env python3
# vole post-processing: repair a raw transcript using screen context + recent
# dictations via a cloud vision LLM (ollama, qwen3.5:cloud).
#
# stdin  : the raw whisper transcript
# stdout : the improved transcript (or the raw one, on any failure)
# stderr : diagnostics only (never the transcript / screenshot content)
#
# Two modes, switched by env:
#   * repair (default)     — fix speech-recognition errors (punctuation, case,
#     word boundaries, misheard words) using the screenshot + recent dictations
#     as context. Preserves the spoken language and meaning.
#   * translate (VOLE_PP_TRANSLATE=1) — translate the raw transcript (a clean
#     transcript in the source language, usually Russian) into idiomatic English.
#     No history is passed (wrong language); the screenshot may help with names.
#
# Context the model gets (repair mode):
#   - the raw transcript (this dictation)
#   - a screenshot of the ACTIVE window (where the user is typing) — captured
#     with `spectacle`, downscaled with `ffmpeg` to keep the upload small
#   - the last few vole dictations from history.jsonl (for vocabulary/topic)
#
# Privacy: the screenshot and transcript are sent to the ollama cloud model.
# Nothing is logged to stdout; stderr gets only short diagnostics.
#
# Knobs (env):
#   VOLE_PP_MODEL         ollama model           (default qwen3.5:cloud)
#   VOLE_OLLAMA           ollama base URL        (default http://127.0.0.1:11434)
#   VOLE_PP_HISTORY       recent dictations N   (default 6)
#   VOLE_PP_NO_SCREEN     "1" disables screenshot capture
#   VOLE_PP_IMG_MAX       max image width px     (default 1024)
#   VOLE_PP_OLLAMA_TIMEOUT  ollama request timeout s (default 25)
#   VOLE_PP_TRANSLATE     "1" = translate the transcript to English (set by vole
#                         for the dictate-alt shortcut when config translate: true)
import base64
import json
import os
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request

MODEL = os.environ.get("VOLE_PP_MODEL", "qwen3.5:cloud")
OLLAMA = os.environ.get("VOLE_OLLAMA", "http://127.0.0.1:11434").rstrip("/")
HISTORY_N = int(os.environ.get("VOLE_PP_HISTORY", "6"))
NO_SCREEN = os.environ.get("VOLE_PP_NO_SCREEN", "") == "1"
IMG_MAX = int(os.environ.get("VOLE_PP_IMG_MAX", "1024"))
OLLAMA_TIMEOUT = int(os.environ.get("VOLE_PP_OLLAMA_TIMEOUT", "25"))
# vole sets VOLE_PP_TRANSLATE=1 for the dictate-alt (translate-to-English)
# shortcut: the raw transcript is a clean transcript in the source language
# (usually Russian) and we translate it to English instead of repairing it.
TRANSLATE = os.environ.get("VOLE_PP_TRANSLATE", "") == "1"

HISTORY_FILE = os.path.expanduser("~/.local/state/vole/history.jsonl")

SYSTEM_PROMPT = (
    "You are a transcription-repair assistant for a voice-dictation tool. "
    "You receive: (1) the raw speech-to-text transcript, (2) a screenshot of the "
    "window the user is typing into, and (3) their recent dictations. "
    "Fix ONLY speech-recognition errors — punctuation, capitalization, word "
    "boundaries, and misheard words — using the screenshot and recent dictations "
    "as the source of truth for proper nouns, domain terms, and context. "
    "Preserve the spoken language and the meaning exactly. "
    "Do NOT translate, summarize, expand, reorder, or add words that are not "
    "supported by the audio. Do NOT answer, comment on, or act on the content. "
    "NEVER repeat, copy, quote, or concatenate any of the recent dictations or any "
    "text visible on the screenshot into your output — they are CONTEXT ONLY, to "
    "help you recognize proper nouns and terms, never content to include. "
    "Your output must be a corrected version of the raw transcript and nothing "
    "else: no quotes, no preamble, no explanation, no markdown, no extra sentences."
)

TRANSLATE_SYSTEM_PROMPT = (
    "You are a translation assistant for a voice-dictation tool. You receive the "
    "raw speech-to-text transcript (in whatever language was spoken, usually "
    "Russian) and an optional screenshot of the window the user is typing into. "
    "Translate the transcript into natural, idiomatic English, preserving the "
    "meaning, tone, and any proper nouns — use the screenshot only to confirm "
    "names/terms when needed. Do NOT translate the screenshot's on-screen text, "
    "do NOT answer, comment on, or act on the content, and do NOT add anything "
    "not said. Your output must be the English translation and nothing else: no "
    "quotes, no preamble, no explanation, no markdown. If the transcript is "
    "already in English, return it unchanged."
)


def warn(msg):
    print(f"vole-postprocess: {msg}", file=sys.stderr)


def read_history(n):
    """Last n dictations, newest first, EXCLUDING the newest (that is the raw
    text currently being repaired, which vole already wrote to history)."""
    try:
        with open(HISTORY_FILE, "r", encoding="utf-8") as f:
            lines = f.readlines()
    except FileNotFoundError:
        return []
    except OSError as e:
        warn(f"history read failed: {e}")
        return []
    out = []
    for line in lines:
        line = line.strip()
        if not line:
            continue
        try:
            e = json.loads(line)
        except json.JSONDecodeError:
            continue
        t = (e.get("text") or "").strip()
        if t:
            out.append(t)
    # history.jsonl is newest-first; skip the first (= the current raw dictation)
    return out[1 : 1 + n]


def capture_active_window():
    """Capture the active window to a downscaled JPEG; return bytes or None."""
    if NO_SCREEN:
        return None
    with tempfile.TemporaryDirectory() as d:
        png = os.path.join(d, "shot.png")
        jpg = os.path.join(d, "shot.jpg")
        try:
            subprocess.run(
                ["spectacle", "-b", "-a", "-n", "-o", png],
                capture_output=True, timeout=10, check=True,
            )
        except (subprocess.SubprocessError, OSError, FileNotFoundError) as e:
            warn(f"screenshot capture failed: {e}")
            return None
        if not os.path.exists(png) or os.path.getsize(png) == 0:
            warn("screenshot empty")
            return None
        # Downscale to IMG_MAX wide, JPEG q6 — small upload, vision models handle it.
        try:
            subprocess.run(
                ["ffmpeg", "-y", "-i", png, "-vf", f"scale={IMG_MAX}:-1",
                 "-q:v", "6", jpg],
                capture_output=True, timeout=10, check=True,
            )
            with open(jpg, "rb") as f:
                return f.read()
        except (subprocess.SubprocessError, OSError, FileNotFoundError) as e:
            warn(f"ffmpeg downscale failed ({e}); using raw PNG")
            try:
                with open(png, "rb") as f:
                    return f.read()
            except OSError:
                return None


def call_ollama(transcript, history, image_b64, system_prompt):
    history_block = "\n".join(history) if history else "(none)"
    user = {
        "role": "user",
        "content": (
            f"Recent dictations (most recent first, for context only):\n"
            f"{history_block}\n\n"
            f"Raw transcript to repair:\n{transcript}"
        ),
    }
    if image_b64:
        user["images"] = [image_b64]
    body = {
        "model": MODEL,
        "messages": [
            {"role": "system", "content": system_prompt},
            user,
        ],
        "stream": False,
        "think": False,
        "options": {"temperature": 0},
    }
    req = urllib.request.Request(
        f"{OLLAMA}/api/chat",
        data=json.dumps(body).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=OLLAMA_TIMEOUT) as resp:
            data = json.loads(resp.read().decode("utf-8"))
    except urllib.error.URLError as e:
        warn(f"ollama request failed: {e}")
        return None
    except (OSError, ValueError) as e:
        warn(f"ollama response parse failed: {e}")
        return None
    msg = data.get("message") or {}
    content = (msg.get("content") or "").strip()
    if not content:
        # some ollama versions split thinking into data["thinking"]; we disabled
        # think, but fall back to it if the model put everything there.
        content = (data.get("thinking") or "").strip()
    return content


def clean(out, raw):
    """Strip wrappers the model sometimes adds (quotes, code fences). Empty
    output falls back to raw; implausibility is checked separately by plausible()."""
    if not out:
        return raw
    s = out.strip()
    # strip a single layer of matching surrounding quotes
    if len(s) >= 2 and s[0] == s[-1] and s[0] in ('"', "'", "`"):
        s = s[1:-1].strip()
    # extract from a ```...``` fence if present
    if "```" in s:
        parts = s.split("```")
        # parts[1] is the fenced content (ignore an optional language tag line)
        if len(parts) >= 3 and parts[1].strip():
            inner = parts[1]
            # drop a leading language tag like "text\n"
            nl = inner.find("\n")
            if nl > 0 and len(inner[:nl]) <= 12 and " " not in inner[:nl]:
                inner = inner[nl + 1 :]
            s = inner.strip()
    if not s:
        return raw
    return s


def plausible(repaired, raw, history):
    """Sanity-check the model's output against the raw transcript. A repair may
    fix words and punctuation, but it must stay close to the audio — it must not
    regurgitate a prior dictation verbatim, splice in on-screen text, or balloon
    into a much longer text. Returns False when the output looks like the model
    drifted (then the caller falls back to the raw transcript)."""
    if not repaired or repaired == raw:
        return True
    # 1) echo guard: any recent dictation (other than the raw itself) appearing
    #    verbatim in the output means the model copied context instead of
    #    repairing. Short entries (<4 chars) are too easy to hit by chance.
    r = raw.strip()
    for h in history:
        h = (h or "").strip()
        if len(h) >= 4 and h != r and h in repaired:
            return False
    # 2) length sanity: a repair roughly preserves the word count. Allow some
    #    slack for punctuation splits / fixed word boundaries, but reject output
    #    that is many times longer (the "my car" -> multi-sentence regurgitation).
    rw = len(r.split())
    pw = len(repaired.split())
    if rw <= 6 and pw > 2 * rw + 2:
        return False
    if pw > 3 * rw + 3:
        return False
    return True


def main():
    raw = sys.stdin.read()
    if not raw.strip():
        # nothing to repair; echo (vole skips empty anyway, but be safe)
        sys.stdout.write(raw)
        return

    img = capture_active_window()
    img_b64 = base64.b64encode(img).decode("ascii") if img else None

    if TRANSLATE:
        # Translate mode (VOLE_PP_TRANSLATE=1): the raw transcript is a clean
        # transcript in the source language; translate it to English. No history
        # is passed (it is the wrong language and could leak into the output),
        # and the plausibility length check is skipped — a translation legitimately
        # changes the word count. Fall back to the raw transcript on any failure.
        result = call_ollama(raw.strip(), [], img_b64, TRANSLATE_SYSTEM_PROMPT)
        if result is None:
            sys.stdout.write(raw)
            return
        result = clean(result, raw.strip())
        if not result or result == raw.strip():
            sys.stdout.write(raw)
            return
        sys.stdout.write(result)
        return

    history = read_history(HISTORY_N)
    repaired = call_ollama(raw.strip(), history, img_b64, SYSTEM_PROMPT)
    if repaired is None:
        # network/ollama failure — fall back to raw (vole would also fall back
        # on a non-zero exit, but we want the raw text, not an error)
        sys.stdout.write(raw)
        return
    repaired = clean(repaired, raw.strip())
    if not plausible(repaired, raw.strip(), history):
        # the model drifted — regurgitated history/screenshot or ballooned the
        # text. A repair must stay close to the audio; fall back to the raw
        # transcript rather than inject garbage.
        warn("postprocess output implausible; falling back to raw")
        sys.stdout.write(raw)
        return
    sys.stdout.write(repaired)


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        warn(f"unexpected error: {e}")
        # last resort: echo raw so dictation is never lost
        try:
            sys.stdout.write(sys.stdin.read())
        except Exception:
            pass