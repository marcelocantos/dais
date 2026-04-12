// Transport abstraction for jevons web UI.
//
// Two implementations:
//   - WebSocketTransport: browser mode, real bytes over WebSocket
//   - NativeTransport: iOS WKWebView mode, JS bridge with native audio handles
//
// The web UI codes against the Transport interface and doesn't know
// which implementation is active.

// --- Transport interface ---
//
// Methods:
//   connect()
//   disconnect()
//   send(json)                  — send JSON text message
//   startVoice()                — open voice session
//   stopVoice()                 — close voice session
//   sendAudio(handleOrBytes)    — send audio (ArrayBuffer or native handle)
//   playAudio(handleOrBytes)    — play audio (ArrayBuffer or native handle)
//
// Callbacks (set by consumer):
//   onOpen()
//   onClose()
//   onMessage(json)             — incoming JSON text
//   onMicFrame({handle, rms})   — mic audio frame available for VAD
//   onAudio(handleOrBytes)      — incoming audio from Grok
//   onVoiceEvent(json)          — voice status/transcript events
//   onError(string)             — error message

// --- Detect environment ---
const isNative = !!(window.webkit?.messageHandlers?.jevons);

// ============================================================
// WebSocket transport (browser mode)
// ============================================================
class WebSocketTransport {
  constructor() {
    this.ws = null;
    this.voiceWs = null;
    this.onOpen = null;
    this.onClose = null;
    this.onMessage = null;
    this.onMicFrame = null;
    this.onAudio = null;
    this.onVoiceEvent = null;
    this.onError = null;

    // Mic capture state.
    this._audioCtx = null;
    this._mediaStream = null;
    this._scriptNode = null;
  }

  connect() {
    const h = location.hostname === 'localhost'
      ? '127.0.0.1:' + location.port : location.host;
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    this.ws = new WebSocket(proto + '//' + h + '/ws/chat');
    this.ws.onopen = () => this.onOpen?.();
    this.ws.onclose = () => this.onClose?.();
    this.ws.onmessage = e => {
      try { this.onMessage?.(JSON.parse(e.data)); }
      catch (x) { console.error('transport: parse error', x); }
    };
  }

  disconnect() {
    this.ws?.close();
    this.ws = null;
    this.stopVoice();
  }

  send(text) {
    if (this.ws?.readyState === 1) this.ws.send(text);
  }

  startVoice() {
    const h = location.hostname === 'localhost'
      ? '127.0.0.1:' + location.port : location.host;
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    this.voiceWs = new WebSocket(proto + '//' + h + '/ws/voice');
    this.voiceWs.binaryType = 'arraybuffer';

    this.voiceWs.onopen = () => {
      this.onVoiceEvent?.({type: 'status', status: 'connected'});
      this._startMicCapture();
    };
    this.voiceWs.onclose = () => {
      this.stopVoice();
    };
    this.voiceWs.onerror = () => {
      this.stopVoice();
    };
    this.voiceWs.onmessage = e => {
      if (e.data instanceof ArrayBuffer) {
        this.onAudio?.(e.data);
      } else {
        try { this.onVoiceEvent?.(JSON.parse(e.data)); }
        catch (x) { console.error('voice msg parse', x); }
      }
    };
  }

  stopVoice() {
    this._stopMicCapture();
    if (this.voiceWs) { this.voiceWs.close(); this.voiceWs = null; }
  }

  sendAudio(buffer) {
    // buffer is an ArrayBuffer — send as binary WebSocket frame.
    if (this.voiceWs?.readyState === 1) this.voiceWs.send(buffer);
  }

  playAudio(buffer) {
    // buffer is an ArrayBuffer of PCM16 24kHz mono.
    if (!this._audioCtx) this._audioCtx = new AudioContext({sampleRate: 24000});
    const pcm16 = new Int16Array(buffer);
    const float32 = new Float32Array(pcm16.length);
    for (let i = 0; i < pcm16.length; i++) {
      float32[i] = pcm16[i] / (pcm16[i] < 0 ? 0x8000 : 0x7FFF);
    }
    this._playbackQueue.push(float32);
    if (!this._isPlaying) this._drainPlayback();
  }

  // --- Internal: mic capture ---

  async _startMicCapture() {
    try {
      this._mediaStream = await navigator.mediaDevices.getUserMedia({
        audio: {sampleRate: 48000, channelCount: 1, echoCancellation: true, noiseSuppression: true}
      });
    } catch (err) {
      this.onError?.('Mic access denied: ' + err.message);
      return;
    }

    if (!this._audioCtx) this._audioCtx = new AudioContext({sampleRate: 48000});
    if (this._audioCtx.state === 'suspended') await this._audioCtx.resume();

    const source = this._audioCtx.createMediaStreamSource(this._mediaStream);
    this._scriptNode = this._audioCtx.createScriptProcessor(2048, 1, 1);

    this._scriptNode.onaudioprocess = e => {
      const input = e.inputBuffer.getChannelData(0);

      // Compute RMS.
      let sum = 0;
      for (let i = 0; i < input.length; i++) sum += input[i] * input[i];
      const rms = Math.sqrt(sum / input.length);

      // Downsample to 24kHz PCM16.
      const ratio = this._audioCtx.sampleRate / 24000;
      const outLen = Math.floor(input.length / ratio);
      const pcm16 = new Int16Array(outLen);
      for (let i = 0; i < outLen; i++) {
        const idx = Math.floor(i * ratio);
        const s = Math.max(-1, Math.min(1, input[idx]));
        pcm16[i] = s < 0 ? s * 0x8000 : s * 0x7FFF;
      }

      // In browser mode, the handle IS the buffer.
      this.onMicFrame?.({handle: pcm16.buffer, rms});
    };

    source.connect(this._scriptNode);
    this._scriptNode.connect(this._audioCtx.destination);
  }

  _stopMicCapture() {
    if (this._scriptNode) { this._scriptNode.disconnect(); this._scriptNode = null; }
    if (this._mediaStream) {
      this._mediaStream.getTracks().forEach(t => t.stop());
      this._mediaStream = null;
    }
  }

  // --- Internal: audio playback queue ---

  _playbackQueue = [];
  _isPlaying = false;

  _drainPlayback() {
    if (this._playbackQueue.length === 0) { this._isPlaying = false; return; }
    this._isPlaying = true;
    const samples = this._playbackQueue.shift();
    if (!this._audioCtx) this._audioCtx = new AudioContext({sampleRate: 24000});
    const buf = this._audioCtx.createBuffer(1, samples.length, 24000);
    buf.copyToChannel(samples, 0);
    const src = this._audioCtx.createBufferSource();
    src.buffer = buf;
    src.connect(this._audioCtx.destination);
    src.onended = () => this._drainPlayback();
    src.start();
  }
}

// ============================================================
// Native transport (iOS WKWebView mode)
// ============================================================
class NativeTransport {
  constructor() {
    this.onOpen = null;
    this.onClose = null;
    this.onMessage = null;
    this.onMicFrame = null;
    this.onAudio = null;
    this.onVoiceEvent = null;
    this.onError = null;

    // Register global callback for Swift → JS messages.
    window._jevonsTransport = this;
  }

  connect() {
    this._post({action: 'connect'});
  }

  disconnect() {
    this._post({action: 'disconnect'});
  }

  send(text) {
    this._post({action: 'send', data: text});
  }

  startVoice() {
    this._post({action: 'startVoice'});
  }

  stopVoice() {
    this._post({action: 'stopVoice'});
  }

  sendAudio(handle) {
    // handle is a string reference to native buffer.
    this._post({action: 'sendAudio', handle: handle});
  }

  playAudio(handle) {
    // handle is a string reference to native buffer.
    this._post({action: 'playAudio', handle: handle});
  }

  // --- Swift → JS entry points (called via evaluateJavaScript) ---

  // Called by Swift when connected to jevonsd.
  _onOpen() { this.onOpen?.(); }

  // Called by Swift when disconnected.
  _onClose() { this.onClose?.(); }

  // Called by Swift with a JSON message from jevonsd.
  _onMessage(json) { this.onMessage?.(json); }

  // Called by Swift with a mic frame (handle + RMS, no bytes).
  _onMicFrame(handle, rms) { this.onMicFrame?.({handle, rms}); }

  // Called by Swift with incoming audio handle from Grok.
  _onAudio(handle) { this.onAudio?.(handle); }

  // Called by Swift with voice status/transcript events.
  _onVoiceEvent(json) { this.onVoiceEvent?.(json); }

  // Called by Swift with error.
  _onError(msg) { this.onError?.(msg); }

  _post(msg) {
    window.webkit.messageHandlers.jevons.postMessage(msg);
  }
}

// ============================================================
// Export the appropriate transport
// ============================================================
const transport = isNative ? new NativeTransport() : new WebSocketTransport();
