package main

import (
	"bytes"
	"strings"
)

// maxSSELineBytes bounds the reassembly buffer. Image SSE frames carrying
// progressive base64 previews are legitimately large, so the guard is generous;
// it only exists so a stream that never emits a newline cannot grow forever.
const maxSSELineBytes = 16 << 20

// sseLineScanner reassembles logical SSE lines across host chunk boundaries.
//
// The host hands us arbitrary byte slices, so a single "data: {...}" line can be
// split across two chunks. Splitting each chunk independently (the previous
// behaviour) silently corrupted those lines.
//
// The []byte passed to fn aliases the internal buffer and is only valid for the
// duration of the call — copy it (or convert to string) if you need to keep it.
type sseLineScanner struct {
	buf []byte
}

func (s *sseLineScanner) feed(chunk []byte, fn func(line []byte)) {
	s.buf = append(s.buf, chunk...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			break
		}
		fn(s.buf[:i])
		// Copy the remainder down instead of reslicing, so the backing array is
		// reused rather than growing without bound as we advance.
		s.buf = append(s.buf[:0], s.buf[i+1:]...)
	}
	if len(s.buf) > maxSSELineBytes {
		fn(s.buf)
		s.buf = s.buf[:0]
	}
}

// flush emits any trailing bytes not terminated by a newline.
func (s *sseLineScanner) flush(fn func(line []byte)) {
	if len(s.buf) > 0 {
		fn(s.buf)
		s.buf = s.buf[:0]
	}
}

// sseData strips the "data:" prefix and surrounding space, returning nil for
// blank lines and the [DONE] sentinel.
func sseData(line []byte) []byte {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil
	}
	// The host may already strip "data:" and pass raw JSON lines.
	if bytes.HasPrefix(line, []byte("data:")) {
		line = bytes.TrimSpace(line[5:])
	}
	if len(line) == 0 || bytes.Equal(line, []byte("[DONE]")) {
		return nil
	}
	return line
}

// imageIDCollector extracts image ids from SSE payloads incrementally so the raw
// payloads never have to be retained. Previously every SSE line was appended to a
// []string that stayed live through the whole poll loop (up to ImagePollTimeoutSecs),
// which for image streams carrying base64 preview frames meant tens of MB held for
// minutes.
type imageIDCollector struct {
	fileIDs      []string
	sedimentIDs  []string
	seenFile     map[string]struct{}
	seenSediment map[string]struct{}
	events       int
	lastPreview  string
}

func newImageIDCollector() *imageIDCollector {
	return &imageIDCollector{
		seenFile:     map[string]struct{}{},
		seenSediment: map[string]struct{}{},
	}
}

func (c *imageIDCollector) addFile(ids ...string) {
	for _, id := range ids {
		if id == "" || id == "file_upload" {
			continue
		}
		if _, ok := c.seenFile[id]; ok {
			continue
		}
		c.seenFile[id] = struct{}{}
		c.fileIDs = append(c.fileIDs, id)
	}
}

func (c *imageIDCollector) addSediment(ids ...string) {
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := c.seenSediment[id]; ok {
			continue
		}
		c.seenSediment[id] = struct{}{}
		c.sedimentIDs = append(c.sedimentIDs, id)
	}
}

// feed scans one payload and keeps only the ids. The payload itself is dropped;
// a truncated copy of the most recent one is retained for error reporting.
func (c *imageIDCollector) feed(payload string) {
	c.events++
	c.lastPreview = truncate(payload, 300)

	for _, m := range fileServiceIDRe.FindAllStringSubmatch(payload, -1) {
		if len(m) > 1 {
			c.addFile(m[1])
		}
	}
	c.addFile(realImageFileIDRe.FindAllString(payload, -1)...)
	for _, m := range genericFileIDRe.FindAllStringSubmatch(payload, -1) {
		if len(m) > 1 && !strings.HasPrefix(m[1], "file-service") {
			// keep real image file ids + file-service extracted, skip other noise
			if strings.Contains(m[1], "file_00000000") || strings.HasPrefix(m[1], "file-") {
				c.addFile(m[1])
			}
		}
	}
	for _, m := range sedimentIDRe.FindAllStringSubmatch(payload, -1) {
		if len(m) > 1 {
			c.addSediment(m[1])
		}
	}
}

// extractImageIDsFromPayloads keeps the batch entry point for callers that
// already hold a bounded set of payloads (conversation JSON polling).
func extractImageIDsFromPayloads(payloads []string) (fileIDs, sedimentIDs []string) {
	c := newImageIDCollector()
	for _, p := range payloads {
		c.feed(p)
	}
	return c.fileIDs, c.sedimentIDs
}
