/* Minimal voice surface for samantha serve (WI-62e19b).
 * V1: text / iOS dictation in, pcm_s16le audio_chunk playback out.
 * Mic uplink is intentionally deferred.
 */
(function () {
  const $ = (id) => document.getElementById(id);
  const tokenEl = $("token");
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

  function playPCM(base64, sampleRate) {
    if (!audioCtx || !base64) return;
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

    if (ws) {
      try { ws.close(); } catch (_) {}
      ws = null;
    }

    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const url =
      proto +
      "//" +
      location.host +
      "/v1/stream?token=" +
      encodeURIComponent(token);

    setStatus("connecting…");
    ws = new WebSocket(url);

    ws.onopen = () => {
      setConnected(true);
      setStatus("connected", true);
      log("connected — audio stream on", "sys");
      sendJSON({ type: "audio_output", mode: "stream" });
      nextPlayTime = 0;
    };

    ws.onclose = () => {
      setConnected(false);
      setStatus("disconnected");
      log("disconnected", "sys");
    };

    ws.onerror = () => {
      log("websocket error", "err");
    };

    ws.onmessage = (ev) => {
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
          playPCM(env.data, env.sample_rate);
          break;
        case "audio_end":
          // gapless scheduler already tracks duration
          break;
        case "turn_interrupted":
          log("interrupted", "sys");
          nextPlayTime = 0;
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
    sendJSON({ type: "interrupt" });
    nextPlayTime = 0;
    if (audioCtx) {
      // hard stop: recreate context is heavy; just reset schedule
      nextPlayTime = audioCtx.currentTime;
    }
  });

  clearBtn.addEventListener("click", () => {
    sendJSON({ type: "clear_history" });
  });
})();
