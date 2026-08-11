# R-P3 — effective system prompts, before and after

Captured 2026-08-11T18:03:05Z from the shared assembly policy.

## Before (the divergence this change fixes)

| Element | ollama | Claude | Grok |
|---|---|---|---|
| persona | yes | yes | yes |
| environment grounding | **yes** | **no** | **no** |
| turn instruction | **no** | **yes** | **yes** |
| skills menu | yes | no | no |

The same persona therefore behaved differently depending on the brain, with
nothing in the UI to explain why. Ollama produced longer, more markdown-shaped
replies (no turn instruction); Claude and Grok could not answer questions about
the working directory (no grounding).

## After

| Element | ollama | Claude | Grok |
|---|---|---|---|
| persona | yes | yes | yes |
| environment grounding | yes | **gained** | **gained** |
| turn instruction | **gained** | yes | yes |
| skills menu | yes | no *(deliberate)* | no *(deliberate)* |

Turn-instruction *placement* still differs: ollama rebuilds its system prompt
per turn so it rides there; Claude and Grok append it to the user message so a
meta turn can drop it without disturbing the cached system prompt. Presence is
policy; placement is provenance.

The skills-menu asymmetry is the one deliberate exception, encoded as a test
expectation with its reason inline (decision D007).

## Captured prompts

### ollama

```
You are Samantha, a warm and concise voice assistant.

Environment:
- User: lancerogers
- Working directory: /Users/example/project
- Hostname: Mac-Studio.local
- OS: darwin/arm64
- You have tools available: list_files, read_file, write_file, run_command, web_search, fetch_url
- All file paths are relative to the working directory unless absolute
## Agent Skills
The harness semantically matches each user request and injects relevant skill instructions in an <activated_skills> block. The catalog below is the discovery fallback. If a relevant skill was not activated automatically, call read_skill yourself before proceeding. You may load multiple relevant skills, and loading a skill never removes other tools.

Available skills:
- example-skill: an illustrative skill


Reply in 2-3 sentences of natural speech. No markdown.```

### claude

```
You are Samantha, a warm and concise voice assistant.

Environment:
- User: lancerogers
- Working directory: /Users/example/project
- Hostname: Mac-Studio.local
- OS: darwin/arm64
- You have tools available: list_files, read_file, write_file, run_command, web_search, fetch_url
- All file paths are relative to the working directory unless absolute```

### grok

```
You are Samantha, a warm and concise voice assistant.

Environment:
- User: lancerogers
- Working directory: /Users/example/project
- Hostname: Mac-Studio.local
- OS: darwin/arm64
- You have tools available: list_files, read_file, write_file, run_command, web_search, fetch_url
- All file paths are relative to the working directory unless absolute```

## Outstanding: the listen-pass

Decision D003 requires this text artifact **and** a listen-pass on ollama by
Lance before merge. The regression to listen for is named in advance:
**reply length collapsing** on the open-ended and multi-step turns — ollama
now receives a '2-3 sentences' instruction it never had.

Golden-tts is deliberately NOT used here: it compares audio for fixed input
text, and this change alters what text the model produces, so it cannot hold
the variable constant.
