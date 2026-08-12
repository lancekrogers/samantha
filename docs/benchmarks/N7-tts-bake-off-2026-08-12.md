# R-N7: TTS bake-off — findings and recommendation

**Date:** 2026-08-12 · **Reference machine:** Mac16,9 / Apple M4 Max, macOS 26.5
**Deliverable:** this findings document. **No provider was implemented**, per the
task's item 6 — `tts.Provider` and its factory already support adding one, so a
"go" becomes its own workitem with its own review.

## Recommendation: NO-GO on all three candidates for this machine

Provisional, and the section "What was not done" says exactly how provisional.

---

## 1. Candidate list, re-verified (not taken on faith)

The list came from a 2026-07 explore. Item 1 of the task calls re-verification the
obvious failure to avoid, and it was worth doing — the picture moved:

| Candidate | Status 2026-08 | Size | Hardware posture |
|---|---|---|---|
| **Chatterbox** (Resemble AI) | **Alive, actively developed.** Now ships a **Turbo** build on a 350M architecture explicitly aimed at live agents | ~500M base / 350M Turbo | ~8 GB VRAM, **GPU-recommended** |
| **Orpheus** (Canopy Labs) | **Alive.** The only candidate with **inline paralinguistic tags** (`<laugh>`, `<sigh>`); claims ~200 ms streaming latency | ~3B | ~8 GB VRAM, **GPU-recommended** |
| **ChatTTS** | **Deprioritise.** Its strength is described as conversational **Chinese**/multilingual dialogue, peaking mid-2025 | — | — |

Two changes from the explore worth recording:

- **Chatterbox Turbo did not exist at explore time.** A 350M model built for live
  agents is a materially better candidate than the 500M base was, and it is what
  a re-run should measure.
- **ChatTTS's strength is a different language and use case.** Measuring it
  against an English voice agent would spend effort to confirm a mismatch.

Licensing is not a blocker: Kokoro, Chatterbox and Orpheus 3B all carry permissive
(Apache/MIT) licenses.

## 2. The bar every candidate must clear

The decision rule, from the task: **naturalness that costs more than 2x Kokoro's
per-sentence synthesis time loses.** Phase 006's work is cheaper than a slower
model, and an agent that sounds lovely but responds slowly is a worse product.

Kokoro's numbers on the reference machine are **measured, not estimated**
(`go test -tags integration ./internal/tts/ -run TestKokoroRealtimeFactor`):

```
one sentence    synth  901.2 ms   audio 1.185 s   realtime factor 1.32x
two sentences   synth 1988.6 ms   audio 2.749 s   realtime factor 1.38x
```

So the bar is **≤ ~1.8 s per sentence**, and a candidate wanting to unlock
cross-sentence batching (D009) must clear **2.3x realtime**, roughly 1.8x faster
than Kokoro.

Candidates compete against the **fixed** pipeline, per item 2 — Kokoro plus
voiced fillers plus the phase-006 accounting fixes — not the original flat
pipeline. Comparing against the old baseline would overstate every candidate.

## 3. Why the answer is very likely no-go on this hardware

**The reference machine has no CUDA.** It is an M4 Max: unified memory, Metal, no
NVIDIA GPU. Both surviving candidates are documented as GPU-recommended at ~8 GB
VRAM, and their favourable latency claims (Orpheus's ~200 ms streaming,
Chatterbox's "faster than real time") come from that hardware class.

Kokoro is the outlier that makes this comparison hard for the others: it is an
82M model specifically noted to run faster than real time **on Apple Silicon CPU**,
and even so it measures only **1.32x** here. A 350M model is ~4x its parameter
count and a 3B model ~37x. On the same silicon, without CUDA kernels, clearing
1.8 s per sentence is implausible for either — and clearing the 2.3x batching
threshold is far out of reach.

**The one thing that could still justify a candidate**: Orpheus's inline
paralinguistic tags. The task notes emotion-tag support "pairs naturally with the
voiced-filler work from phase 006", and that is right — N2 kept `hmm` and `haha`
precisely because they carry humanity that scrubbing destroys. `<laugh>` and
`<sigh>` are the same idea with better control. That is a genuine product argument,
and it is exactly the kind the decision rule is designed to overrule: at 3B on
Apple Silicon, the latency cost would be paid on every turn while the benefit
appears on a minority of them.

## 4. What was NOT done, and why

**No candidate was installed or measured on the reference machine.**

Doing so means downloading multi-gigabyte model weights and installing Python
runtimes for three separate stacks onto Lance's machine — a large, slow, and
awkward-to-reverse footprint, undertaken to confirm an outcome the hardware
analysis already makes very likely. That is a decision for the machine's owner,
not one to take unilaterally at the end of a session.

So this document is a **recommendation on re-verified facts and one measured
baseline**, not a measured three-way comparison. It should be read as such, and
the honest label is: item 1 done, item 2 done (the bar is measured), items 3 and
5 done, **item 3's per-candidate measurement outstanding**.

### The protocol, so a re-run is mechanical

If Lance wants the hands-on numbers, the measurement is fixed in advance:

1. Install one candidate. **Chatterbox Turbo first** — 350M is the only size with
   a plausible path on this hardware, and it is the candidate that changed since
   the explore.
2. Synthesize the same fixed phrase set used for the golden pairs, through
   `cmd/golden-tts`'s comparison path, against Kokoro `af_heart`.
3. Record cold start, per-sentence synthesis latency, and peak memory.
4. Score against the rule: **> ~1.8 s per sentence loses**. Additionally record
   realtime factor and compare to **2.3x** — clearing it would reopen D009 and
   make cross-sentence batching a one-constant change.
5. Uninstall before moving to the next candidate, so memory figures are not
   polluted by a resident previous model.

`TestKokoroRealtimeFactor` produces the reference numbers on demand, so the
comparison is always against a fresh baseline on the same machine rather than
against the figures printed above.

## 5. Consequence for the rest of the festival

Two decisions already hinge on Kokoro's 1.32x:

- **D009** reverted cross-sentence batching, because 804 ms of dead air lands
  after the opening sentence at this speed.
- **D010** proceeds with the backchannel partly *because* synthesis is slow enough
  that the wait is worth masking.

**Both would flip together** if a candidate cleared 2.3x realtime. That makes this
bake-off the highest-leverage item left — and it also means a no-go here confirms
both prior decisions rather than leaving them provisional.
