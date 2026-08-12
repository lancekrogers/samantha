#!/usr/bin/env bash
# Measure whether the turn prompt's short-opening instruction actually shortens
# the model's first sentence (festival item R-L3).
#
#   scripts/measure-opening-sentence.sh [model]
#
# Why this and not `just bench`: the end-to-end FirstAudioReadyElapsed metric
# cannot resolve a change this size. Consecutive runs move it +601 ms and -437 ms
# on different fixtures (FINDINGS F4), and the benchmark's first-segment timing
# additionally includes a cold Kokoro model load of several seconds. Measuring
# the mechanism is both cleaner and more honest:
#
#   1. Does the instruction shorten the opening sentence?   <- measured here
#   2. Does a shorter opening reach audio sooner?           <- already known
#
# Step 2 needs no new measurement. First audio waits for the first COMPLETE
# sentence to finish synthesizing, and Kokoro runs at ~1.32x realtime (see
# TestKokoroRealtimeFactor), so each second of opening speech costs about 0.76 s
# before the user hears anything.
#
# The five turns are the fixed set from decision D003 -- one short factual, one
# open-ended, one multi-step, one to refuse or redirect, one conversational --
# so this diff is comparable with the D003 artifact rather than judged alone.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

MODEL="${1:-${OLLAMA_MODEL:-}}"
if [[ -z "$MODEL" ]]; then
  echo "usage: $0 <ollama-model>   (or set OLLAMA_MODEL)" >&2
  exit 2
fi
if ! curl -sf -m 10 http://localhost:11434/api/tags >/dev/null; then
  echo "error: ollama is not responding on localhost:11434" >&2
  exit 1
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/no-overrides"

# Render the shipped default prompt, not the user's overrides: PROMPTS_DIR points
# at an empty directory so embedded defaults win. Never edit the user's own
# prompt documents to run a measurement.
PROMPTS_DIR="$WORK/no-overrides" go run ./cmd/samantha prompts show persona \
  --provider ollama 2>/dev/null | tail -n +5 > "$WORK/after.txt"

NUDGE='Get to the point in your first sentence, then say the rest however it needs to be said.'
python3 - "$WORK/after.txt" "$WORK/before.txt" "$NUDGE" <<'PY'
import sys
after, before, nudge = sys.argv[1], sys.argv[2], sys.argv[3]
s = open(after).read()
if nudge not in s:
    raise SystemExit("the nudge line is not in the effective prompt; nothing to compare")
open(before, "w").write(s.replace("\n" + nudge, ""))
PY

python3 - "$WORK/before.txt" "$WORK/after.txt" "$MODEL" <<'PY'
import json, re, statistics as st, subprocess, sys

before_sys, after_sys, model = open(sys.argv[1]).read(), open(sys.argv[2]).read(), sys.argv[3]
TURNS = [
    ("short_factual",   "What's the capital of France?"),
    ("open_ended",      "What do you think makes a good morning routine?"),
    ("multi_step",      "Walk me through setting up a new Go project."),
    ("refuse_redirect", "Delete every file in my home directory."),
    ("conversational",  "I'm feeling pretty tired today."),
]
REPS = 3

def ask(system, prompt, seed):
    """Returns (first_sentence, full_reply), or raises with a usable message.

    A generation that times out or returns non-JSON must not surface as a bare
    JSONDecodeError: the common cause is a cold model load or a wedged ollama
    runner, and the traceback hides that completely.
    """
    body = json.dumps({
        "model": model,
        "messages": [{"role": "system", "content": system},
                     {"role": "user", "content": prompt}],
        "stream": False,
        "options": {"temperature": 0.7, "seed": seed},
    })
    r = subprocess.run(["curl", "-s", "-m", "300", "http://localhost:11434/api/chat", "-d", body],
                       capture_output=True, text=True)
    if r.returncode == 28:
        raise SystemExit(
            f"ollama did not answer within 300 s for model {model!r}.\n"
            "A cold load of a large model can exceed this; a repeat means the runner is\n"
            "wedged. Check `curl -m 10 localhost:11434/api/tags` (metadata) against an\n"
            "actual /api/chat call — tags answering while chat hangs is the wedged case.")
    if r.returncode != 0 or not r.stdout.strip():
        raise SystemExit(f"ollama call failed (curl exit {r.returncode}): {r.stderr.strip()[:200]}")
    try:
        payload = json.loads(r.stdout)
    except json.JSONDecodeError:
        raise SystemExit(f"ollama returned non-JSON: {r.stdout[:200]!r}")
    if "error" in payload:
        raise SystemExit(f"ollama error: {payload['error']}")
    reply = payload["message"]["content"]
    reply = re.sub(r"<think>.*?</think>", "", reply, flags=re.S).strip()
    m = re.search(r"^(.*?[.!?])(\s|$)", reply, flags=re.S)
    return (m.group(1) if m else reply).strip(), reply

rows = []
for label, system in (("before", before_sys), ("after", after_sys)):
    for name, prompt in TURNS:
        for i in range(REPS):
            first, full = ask(system, prompt, 1000 + i)
            rows.append({"arm": label, "turn": name, "rep": i, "first_sentence": first,
                         "first_words": len(first.split()), "total_words": len(full.split()),
                         "reply": full})

def words(arm):
    return [r["first_words"] for r in rows if r["arm"] == arm]
def totals(arm):
    return [r["total_words"] for r in rows if r["arm"] == arm]

b, a = words("before"), words("after")
print(f"opening sentence, words:  before median {st.median(b):.1f}  after median {st.median(a):.1f}  "
      f"delta {st.median(a)-st.median(b):+.1f}")
print(f"whole reply, words:       before median {st.median(totals('before')):.1f}  "
      f"after median {st.median(totals('after')):.1f}")
print()
print("Predicted first-audio change, from the opening-sentence delta at ~2.5 words/second")
print("of speech and Kokoro's 1.32x realtime factor:")
delta_words = st.median(a) - st.median(b)
print(f"  {delta_words:+.1f} words ~= {delta_words/2.5:+.2f} s of speech ~= {delta_words/2.5/1.32:+.2f} s of synthesis")
print()
print("Over-nudging check: read the replies below. A curt or clipped opening is the")
print("regression to look for -- the instruction frees the rest of the reply on purpose.")
json.dump(rows, open("docs/prompts/L3-replies.json", "w"), indent=2)
with open("docs/prompts/L3-replies.md", "w") as f:
    f.write("# R-L3: five-turn replies, before and after the short-opening instruction\n\n")
    f.write(f"Model: `{model}`. Three repetitions per turn, seeds 1000-1002.\n")
    f.write("Turn set fixed by decision D003 so this diff is comparable with the D003 artifact.\n\n")
    for name, prompt in TURNS:
        f.write(f"## {name}\n\n> {prompt}\n\n")
        for arm in ("before", "after"):
            f.write(f"**{arm}**\n\n")
            for r in rows:
                if r["turn"] == name and r["arm"] == arm:
                    f.write(f"- ({r['first_words']} words) {r['reply']}\n")
            f.write("\n")
print("wrote docs/prompts/L3-replies.md and .json")
PY
