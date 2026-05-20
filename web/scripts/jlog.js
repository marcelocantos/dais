// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Browser-side logger that forwards events to jevonsd's /api/log so
// client-side diagnostics interleave with server logs in /tmp/jevonsd.log.
// Useful for correlating browser state (PTT engagement, AudioContext
// transitions, voice-WS readyState) with server events.
//
// Usage:
//   jLog('info',  'ptt engage', {vadReset: true});
//   jLog('warn',  'voice WS closed unexpectedly');
//   jLog('error', 'commit failed', {readyState});
//
// Posts are fire-and-forget; failures are swallowed (network issues
// shouldn't break the UI). The same message is also emitted to the
// browser console so devtools still works.

(function () {
  'use strict';

  const VALID = new Set(['info', 'warn', 'error', 'debug']);

  // Browser-side console method to mirror level → console.
  const CONSOLE_FOR = {
    info:  (typeof console !== 'undefined' && console.info)  || console.log,
    warn:  (typeof console !== 'undefined' && console.warn)  || console.log,
    error: (typeof console !== 'undefined' && console.error) || console.log,
    debug: (typeof console !== 'undefined' && console.debug) || console.log,
  };

  /**
   * jLog forwards a structured log entry to jevonsd AND mirrors it to
   * the browser console.
   *
   * @param {'info'|'warn'|'error'|'debug'} level
   * @param {string} msg
   * @param {object} [fields] optional structured fields
   */
  window.jLog = function jLog(level, msg, fields) {
    if (!VALID.has(level)) level = 'info';
    const consoleFn = CONSOLE_FOR[level] || console.log;
    if (fields) consoleFn.call(console, `[jLog/${level}] ${msg}`, fields);
    else        consoleFn.call(console, `[jLog/${level}] ${msg}`);
    try {
      fetch('/api/log', {
        method:  'POST',
        headers: {'Content-Type': 'application/json'},
        body:    JSON.stringify({level, msg, fields: fields || undefined}),
        // keepalive lets the request finish even if the page unloads
        // (useful for unhandled-error reports).
        keepalive: true,
      }).catch(() => {});
    } catch (_) { /* fetch unavailable or threw synchronously — swallow */ }
  };

  // Auto-forward unhandled errors and promise rejections.
  window.addEventListener('error', e => {
    window.jLog('error', 'window.onerror', {
      message:  e.message,
      filename: e.filename,
      lineno:   e.lineno,
      colno:    e.colno,
      stack:    e.error?.stack,
    });
  });
  window.addEventListener('unhandledrejection', e => {
    const reason = e.reason;
    window.jLog('error', 'unhandledrejection', {
      message: reason?.message || String(reason),
      stack:   reason?.stack,
    });
  });
})();
