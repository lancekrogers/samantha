# Meeting audio fixtures

## product-marketing-meeting-90s.wav

First ~90 seconds of a real multi-person product marketing meeting:

- **Source:** [Product Marketing Meeting (weekly) 2021-06-28](https://www.youtube.com/watch?v=lBVtvOpU80Q)
- **Format:** 16 kHz mono PCM WAV (downloaded via `yt-dlp` + `ffmpeg`)
- **Not committed** (large binary; regenerate locally)

```bash
just fetch-meeting-fixture
# or
./scripts/fetch-meeting-fixture.sh
```

Used by `tests/speakerflow` diarization integration tests.
