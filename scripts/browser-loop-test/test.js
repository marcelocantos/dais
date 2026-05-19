// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Headless browser PTT loop test for jevonsd's /ws/voice.
//
// Runs the REAL browser audio pipeline (getUserMedia → MediaStreamSource
// → ScriptProcessor → downsample → WS) — only the live mic is replaced
// by a Chromium fake audio device fed from a stitched WAV. That isolates
// the mic-capture / AudioContext lifecycle from human variability, so we
// can reproduce the multi-turn lockup deterministically.
//
// Run: node scripts/browser-loop-test/test.js [--turns 5] [--headed]
//
// Prereqs:
//   * jevonsd must be running on the configured host (default localhost:13705)
//   * macOS (uses `say` for utterance synthesis)
//   * scripts/browser-loop-test deps installed (`npm install` in this dir)

const { chromium } = require('playwright');
const { execFileSync } = require('child_process');
const fs = require('fs');
const path = require('path');

// ---------- config ----------
const argv = process.argv.slice(2);
const opt = (name, def) => {
  const i = argv.indexOf(`--${name}`);
  if (i === -1) return def;
  const next = argv[i + 1];
  return next && !next.startsWith('--') ? next : true;
};
const config = {
  turns: parseInt(opt('turns', '5'), 10),
  host:  opt('host', 'localhost:13705'),
  headed: !!opt('headed', false),
  utteranceMs: 2500,  // how long Ctrl is held per turn (ms)
  silenceMs:   3500,  // post-release gap before next turn (long enough for response.done)
  fixtureDir:  path.join(__dirname, 'fixtures'),
  sampleRate:  24000,
};
fs.mkdirSync(config.fixtureDir, { recursive: true });

// ---------- step 1: synthesise stitched fixture ----------
//
// Chrome reads --use-file-for-fake-audio-capture once at launch — it
// LOOPS the file but we want a deterministic single play, so we concat
// utterance1 + silence + utterance2 + silence + … into one WAV and time
// our Ctrl presses to align with each utterance segment.

function readWav24kPCM(file) {
  const data = fs.readFileSync(file);
  // Standard 44-byte WAV header for the LEI16 mono format `say` emits.
  // Skip it to get raw PCM16 samples.
  if (data.length < 44) throw new Error(`${file}: too short to be WAV`);
  return data.subarray(44);
}

function synthesise(phrase, outFile) {
  execFileSync('say', [
    '-o', outFile,
    '--data-format=LEI16@24000',
    phrase,
  ]);
  return readWav24kPCM(outFile);
}

function silencePCM(ms) {
  const bytes = Math.floor((ms / 1000) * config.sampleRate) * 2; // PCM16 = 2 bytes/sample
  return Buffer.alloc(bytes);
}

function writeWav(file, pcm, sampleRate) {
  const dataLen = pcm.length;
  const header = Buffer.alloc(44);
  header.write('RIFF', 0);
  header.writeUInt32LE(36 + dataLen, 4);
  header.write('WAVE', 8);
  header.write('fmt ', 12);
  header.writeUInt32LE(16, 16);
  header.writeUInt16LE(1, 20);        // PCM
  header.writeUInt16LE(1, 22);        // mono
  header.writeUInt32LE(sampleRate, 24);
  header.writeUInt32LE(sampleRate * 2, 28);
  header.writeUInt16LE(2, 32);        // block align
  header.writeUInt16LE(16, 34);       // bits per sample
  header.write('data', 36);
  header.writeUInt32LE(dataLen, 40);
  fs.writeFileSync(file, Buffer.concat([header, pcm]));
}

// Layout per turn:
//   [0, utteranceMs] utterance (also extended with silence if shorter)
//   [utteranceMs, utteranceMs+silenceMs] silence
// Total per turn = utteranceMs + silenceMs.
const turnTotalMs = config.utteranceMs + config.silenceMs;

function buildFixture() {
  const phrases = [];
  for (let t = 1; t <= config.turns; t++) {
    phrases.push(`Testing turn number ${t}`);
  }
  console.log(`[fixture] generating ${phrases.length} utterances`);
  const parts = [];
  for (let i = 0; i < phrases.length; i++) {
    const tmp = path.join(config.fixtureDir, `utt-${i + 1}.wav`);
    const utt = synthesise(phrases[i], tmp);
    // Pad each utterance up to utteranceMs so timing is uniform.
    const uttMs = Math.floor((utt.length / 2) * 1000 / config.sampleRate);
    if (uttMs > config.utteranceMs) {
      console.warn(`[fixture] utterance ${i + 1} is ${uttMs} ms — longer than utteranceMs=${config.utteranceMs}; bumping turn window`);
      config.utteranceMs = uttMs + 200;
    }
    const padMs = config.utteranceMs - uttMs;
    parts.push(Buffer.concat([utt, silencePCM(padMs), silencePCM(config.silenceMs)]));
  }
  const stitched = Buffer.concat(parts);
  const out = path.join(config.fixtureDir, 'stitched.wav');
  writeWav(out, stitched, config.sampleRate);
  console.log(`[fixture] stitched -> ${out} (${parts.length} turns × ${turnTotalMs} ms = ${parts.length * turnTotalMs / 1000}s)`);
  return out;
}

// ---------- step 2: drive Chromium ----------

async function run() {
  const wavPath = buildFixture();

  const browser = await chromium.launch({
    headless: !config.headed,
    args: [
      '--use-fake-device-for-media-stream',
      '--use-fake-ui-for-media-stream',
      `--use-file-for-fake-audio-capture=${wavPath}`,
      '--autoplay-policy=no-user-gesture-required',
    ],
  });

  const ctx = await browser.newContext({
    permissions: ['microphone'],
  });
  const page = await ctx.newPage();

  // Capture console + page errors for the report.
  const consoleLog = [];
  page.on('console', m => {
    consoleLog.push(`[${m.type()}] ${m.text()}`);
  });
  page.on('pageerror', err => {
    consoleLog.push(`[pageerror] ${err.message}`);
  });

  // Capture each chat-panel message as it appears.
  await page.exposeFunction('_loopTestBubble', (role, text) => {
    bubbles.push({ role, text, t: Date.now() });
  });
  const bubbles = [];

  console.log(`[harness] navigating to http://${config.host}/`);
  await page.goto(`http://${config.host}/`, { waitUntil: 'domcontentloaded' });

  // Wait for the chat WS to come up and the page to be interactive.
  await page.waitForFunction(() => {
    const dot = document.getElementById('dot');
    return dot && dot.classList.contains('on');
  }, { timeout: 20000 });
  console.log('[harness] chat connected');

  // Install a MutationObserver to feed _loopTestBubble whenever a .msg appears.
  await page.evaluate(() => {
    const msgs = document.getElementById('messages');
    const seen = new WeakSet();
    const report = () => {
      for (const el of msgs.querySelectorAll('.msg')) {
        if (seen.has(el)) continue;
        seen.add(el);
        // Capture role from CSS class; text from element minus the timestamp.
        const role = el.classList.contains('user') ? 'user'
                   : el.classList.contains('jevons') ? 'assistant'
                   : 'unknown';
        const timeEl = el.querySelector('.msg-time');
        const text = (el.textContent || '').replace(timeEl?.textContent || '', '').trim();
        window._loopTestBubble(role, text);
      }
    };
    new MutationObserver(report).observe(msgs, { childList: true, subtree: true });
    report();
  });

  // Drive PTT for each turn — Ctrl press windows must stay aligned with
  // the fixture audio timeline. Each turn occupies exactly turnTotalMs
  // (utteranceMs of audio + silenceMs of gap) regardless of how fast the
  // response comes back, so we wait the full slot before pressing Ctrl
  // for the next turn.
  const results = [];
  const harnessStart = Date.now();
  for (let t = 1; t <= config.turns; t++) {
    const turnStartedAt = Date.now();
    const expectedAudio = `Testing turn number ${numberWord(t)}`.toLowerCase();
    console.log(`[turn ${t}] press Ctrl @ ${(Date.now() - harnessStart) / 1000}s`);
    const bubblesBefore = bubbles.length;

    // Ctrl down: trigger pttArm. Hold for utteranceMs.
    await page.keyboard.down('Control');
    await page.waitForTimeout(config.utteranceMs);
    await page.keyboard.up('Control');
    console.log(`[turn ${t}] release Ctrl @ ${(Date.now() - harnessStart) / 1000}s`);

    // Poll for bubbles until the next-turn deadline. Do NOT break early
    // — drifting ahead would misalign Ctrl with the next fixture segment.
    const turnDeadline = turnStartedAt + (config.utteranceMs + config.silenceMs);
    let userBubble = null;
    let assistantBubble = null;
    while (Date.now() < turnDeadline) {
      const fresh = bubbles.slice(bubblesBefore);
      userBubble = userBubble || fresh.find(b => b.role === 'user');
      assistantBubble = assistantBubble || fresh.find(b => b.role === 'assistant');
      await page.waitForTimeout(100);
    }

    const r = {
      turn: t,
      expectedAudio,
      userBubble: userBubble?.text || null,
      assistantBubble: assistantBubble?.text || null,
      passed: !!(userBubble && assistantBubble),
    };
    results.push(r);
    console.log(`[turn ${t}] passed=${r.passed} user=${JSON.stringify(r.userBubble)} asst=${JSON.stringify(r.assistantBubble)}`);
  }

  // ---------- report ----------
  console.log('\n=== browser-loop-test report ===');
  console.log('turn  passed  user_bubble                                   assistant_bubble');
  for (const r of results) {
    console.log(`${String(r.turn).padEnd(5)} ${String(r.passed).padEnd(7)} ${(r.userBubble || '<missing>').slice(0, 44).padEnd(45)} ${(r.assistantBubble || '<missing>').slice(0, 50)}`);
  }
  console.log();

  // Dump tail of console for diagnostics.
  console.log('=== last 30 console messages ===');
  for (const line of consoleLog.slice(-30)) console.log(line);

  await browser.close();

  const anyFail = results.some(r => !r.passed);
  process.exit(anyFail ? 1 : 0);
}

function numberWord(n) {
  return ['', 'one', 'two', 'three', 'four', 'five',
          'six', 'seven', 'eight', 'nine', 'ten'][n] || String(n);
}

run().catch(err => {
  console.error('harness fatal:', err);
  process.exit(2);
});
