/* Minimal voice surface for samantha serve (WI-62e19b).
 * V1: text / iOS dictation in, pcm_s16le audio_chunk playback out.
 * Mic uplink is intentionally deferred.
 */
(function () {
  const $ = (id) => document.getElementById(id);
  const tokenEl = $("token");
  const pairCodeEl = $("pairCode");
  const pairBtn = $("pair");
  const startBtn = $("start");
  const sendBtn = $("send");
  const interruptBtn = $("interrupt");
  const clearBtn = $("clear");
  const inputEl = $("input");
  const logEl = $("log");
  const statusEl = $("status");

  const TOKEN_KEY = "samantha.serve.token";

  let ws = null;
  let audioCtx = null;
  let nextPlayTime = 0;
  let connected = false;
  let audioSuppressed = true;
  const sourcesBySegment = new Map();
  const canceledSegments = new Set();

  tokenEl.value = localStorage.getItem(TOKEN_KEY) || "";

  function setStatus(text, live) {
    statusEl.textContent = text;
    statusEl.classList.toggle("live", !!live);
  }

  function log(text, cls) {
    const p = document.createElement("div");
    p.className = "msg " + (cls || "sys");
    p.textContent = text;
    logEl.appendChild(p);
    logEl.scrollTop = logEl.scrollHeight;
  }

  function setConnected(on) {
    connected = on;
    inputEl.disabled = !on;
    sendBtn.disabled = !on;
    interruptBtn.disabled = !on;
    clearBtn.disabled = !on;
    startBtn.textContent = on ? "Reconnect" : "Start";
  }

  function ensureAudio() {
    if (!audioCtx) {
      const Ctx = window.AudioContext || window.webkitAudioContext;
      audioCtx = new Ctx();
    }
    if (audioCtx.state === "suspended") {
      return audioCtx.resume();
    }
    return Promise.resolve();
  }

  function segmentKey(segmentID) {
    return segmentID == null ? "unknown" : String(segmentID);
  }

  function forgetSource(key, src) {
    const sources = sourcesBySegment.get(key);
    if (!sources) return;
    sources.delete(src);
    if (sources.size === 0) sourcesBySegment.delete(key);
  }

  function stopSegment(key, markCanceled) {
    if (markCanceled) canceledSegments.add(key);
    const sources = sourcesBySegment.get(key);
    if (!sources) return;
    for (const src of sources) {
      src.onended = null;
      try { src.stop(); } catch (_) {}
      try { src.disconnect(); } catch (_) {}
    }
    sourcesBySegment.delete(key);
  }

  function stopAudio(suppress) {
    if (suppress) audioSuppressed = true;
    for (const key of Array.from(sourcesBySegment.keys())) {
      stopSegment(key, true);
    }
    nextPlayTime = audioCtx ? audioCtx.currentTime : 0;
  }

  function applyAudioReset() {
    stopAudio(false);
    canceledSegments.clear();
    audioSuppressed = false;
  }

  function playPCM(base64, sampleRate, segmentID) {
    const key = segmentKey(segmentID);
    if (!audioCtx || !base64 || audioSuppressed || canceledSegments.has(key)) return;
    const binary = atob(base64);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
    // pcm_s16le little-endian mono
    const view = new DataView(bytes.buffer);
    const n = Math.floor(bytes.length / 2);
    if (n === 0) return;
    const floats = new Float32Array(n);
    for (let i = 0; i < n; i++) {
      floats[i] = view.getInt16(i * 2, true) / 32768;
    }
    const rate = sampleRate || 24000;
    const buffer = audioCtx.createBuffer(1, floats.length, rate);
    buffer.copyToChannel(floats, 0);
    const src = audioCtx.createBufferSource();
    src.buffer = buffer;
    src.connect(audioCtx.destination);
    let sources = sourcesBySegment.get(key);
    if (!sources) {
      sources = new Set();
      sourcesBySegment.set(key, sources);
    }
    sources.add(src);
    src.onended = () => forgetSource(key, src);
    const startAt = Math.max(nextPlayTime, audioCtx.currentTime + 0.02);
    src.start(startAt);
    nextPlayTime = startAt + buffer.duration;
  }

  function sendJSON(obj) {
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    ws.send(JSON.stringify(obj));
  }

  function connect() {
    const token = tokenEl.value.trim();
    if (!token) {
      log("paste the serve token first", "err");
      return;
    }
    localStorage.setItem(TOKEN_KEY, token);

    stopAudio(true);
    canceledSegments.clear();
    if (ws) {
      const previous = ws;
      ws = null;
      try { previous.close(); } catch (_) {}
    }

    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const url =
      proto +
      "//" +
      location.host +
      "/v1/stream?token=" +
      encodeURIComponent(token);

    setStatus("connecting…");
    const socket = new WebSocket(url);
    ws = socket;

    socket.onopen = () => {
      if (ws !== socket) {
        socket.close();
        return;
      }
      setConnected(true);
      setStatus("connected", true);
      log("connected — audio stream on", "sys");
      audioSuppressed = false;
      nextPlayTime = 0;
      socket.send(JSON.stringify({ type: "audio_output", mode: "stream" }));
    };

    socket.onclose = () => {
      if (ws !== socket) return;
      ws = null;
      stopAudio(true);
      setConnected(false);
      setStatus("disconnected");
      log("disconnected", "sys");
    };

    socket.onerror = () => {
      if (ws !== socket) return;
      log("websocket error", "err");
    };

    socket.onmessage = (ev) => {
      if (ws !== socket) return;
      let env;
      try {
        env = JSON.parse(ev.data);
      } catch (_) {
        return;
      }
      switch (env.type) {
        case "user_input":
          log("You: " + (env.text || ""), "you");
          break;
        case "response_ready":
          log("Samantha: " + (env.response || ""), "sam");
          break;
        case "thinking_started":
          log("thinking…", "sys");
          break;
        case "audio_output_ack":
          log("audio: " + (env.mode || "?"), "sys");
          break;
        case "audio_chunk":
          playPCM(env.data, env.sample_rate, env.segment_id);
          break;
        case "audio_end":
          if (env.reason === "interrupted" || env.reason === "error") {
            stopSegment(segmentKey(env.segment_id), true);
          }
          break;
        case "audio_reset":
          applyAudioReset();
          break;
        case "turn_interrupted":
          log("interrupted", "sys");
          stopAudio(false);
          break;
        case "conversation_cleared":
          log("conversation cleared", "sys");
          break;
        case "error":
          log("error: " + (env.message || JSON.stringify(env)), "err");
          break;
        default:
          break;
      }
    };
  }

  pairBtn.addEventListener("click", () => {
    const code = pairCodeEl.value.trim();
    if (!code) {
      log("enter the 6-digit pairing code from the Mac", "err");
      return;
    }
    setStatus("pairing…");
    fetch("/v1/pair", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ code: code }),
    })
      .then(async (resp) => {
        const body = await resp.json().catch(() => ({}));
        if (!resp.ok) {
          throw new Error(body.error || "pair failed (" + resp.status + ")");
        }
        if (!body.token) {
          throw new Error("pair response missing token");
        }
        tokenEl.value = body.token;
        localStorage.setItem(TOKEN_KEY, body.token);
        pairCodeEl.value = "";
        log("paired — token saved on this device", "sys");
        setStatus("paired (token ready)");
      })
      .catch((err) => {
        log("pair failed: " + err.message, "err");
        setStatus("pair failed");
      });
  });

  startBtn.addEventListener("click", () => {
    ensureAudio()
      .then(connect)
      .catch((err) => log("audio unlock failed: " + err, "err"));
  });

  sendBtn.addEventListener("click", () => {
    const text = inputEl.value.trim();
    if (!text) return;
    sendJSON({ type: "text_input", text: text });
    inputEl.value = "";
  });

  inputEl.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      sendBtn.click();
    }
  });

  interruptBtn.addEventListener("click", () => {
    stopAudio(true);
    sendJSON({ type: "interrupt" });
  });

  clearBtn.addEventListener("click", () => {
    sendJSON({ type: "clear_history" });
  });
})();
