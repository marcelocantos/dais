// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package voicelab

import (
	"sync"
	"time"
)

// BufferSource emits a fixed PCM buffer in realtime-sized chunks. Use
// for tests: TTS the utterance, append a short silence tail so Grok's
// server VAD commits, hand the bytes here. Frames pace at SampleRate
// so end-of-utterance latency measurements are meaningful (without
// pacing, a 5 s utterance would dump into Grok in milliseconds and the
// latency floor would be nonsense).
//
// FrameDuration controls the chunk granularity (default 20 ms — a good
// match for typical RealTime APIs).
type BufferSource struct {
	PCM           []byte
	FrameDuration time.Duration

	// StampAfter is a byte offset within PCM. When realtime emission
	// crosses it, OnStamp fires. The harness uses this to record
	// "end-of-utterance audio" precisely (not end-of-silence-tail),
	// so end-to-end latency reads the way the user would experience
	// it: time from "I stopped speaking" to "Grok started speaking".
	StampAfter int
	OnStamp    func()

	startOnce sync.Once
	ch        chan []byte
}

// Frames lazily starts a goroutine that emits chunks at realtime
// cadence, then closes the channel when the buffer is exhausted.
func (b *BufferSource) Frames() <-chan []byte {
	b.startOnce.Do(func() {
		if b.FrameDuration == 0 {
			b.FrameDuration = 20 * time.Millisecond
		}
		samplesPerFrame := int(float64(SampleRate) * b.FrameDuration.Seconds())
		bytesPerFrame := samplesPerFrame * BytesPerSample
		b.ch = make(chan []byte, 4)

		go func() {
			defer close(b.ch)
			ticker := time.NewTicker(b.FrameDuration)
			defer ticker.Stop()

			stamped := b.StampAfter == 0 // disabled if 0
			for offset := 0; offset < len(b.PCM); {
				end := min(offset+bytesPerFrame, len(b.PCM))
				chunk := make([]byte, end-offset)
				copy(chunk, b.PCM[offset:end])
				b.ch <- chunk
				offset = end
				if !stamped && offset >= b.StampAfter {
					stamped = true
					if b.OnStamp != nil {
						b.OnStamp()
					}
				}
				if offset < len(b.PCM) {
					<-ticker.C
				}
			}
		}()
	})
	return b.ch
}

// Close is a no-op; the goroutine exits naturally when the buffer is
// fully emitted.
func (b *BufferSource) Close() error { return nil }

// BufferSink accumulates every PCM frame Grok emits, plus a timestamp
// for the first byte received. The test harness reads FirstFrameAt to
// compute end-to-end latency relative to the last frame sent and the
// concatenated PCM for downstream analysis.
type BufferSink struct {
	mu           sync.Mutex
	pcm          []byte
	firstFrameAt time.Time
}

// Write appends pcm to the buffer. The first write stamps FirstFrameAt.
func (b *BufferSink) Write(pcm []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.firstFrameAt.IsZero() {
		b.firstFrameAt = time.Now()
	}
	b.pcm = append(b.pcm, pcm...)
	return nil
}

// PCM returns a copy of all accumulated audio.
func (b *BufferSink) PCM() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, len(b.pcm))
	copy(out, b.pcm)
	return out
}

// FirstFrameAt returns the wall-clock time of the first OnAudio
// callback. Zero if no audio has arrived yet.
func (b *BufferSink) FirstFrameAt() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.firstFrameAt
}

// Close is a no-op (data lives in memory until the sink is GC'd).
func (b *BufferSink) Close() error { return nil }
