package executor

import (
	"bytes"
	"encoding/json"
	"strings"
)

type SSEEvent struct {
	Done bool
	Data map[string]any
}

type SSEParser struct {
	buffer    []byte
	dataLines []string
}

func (p *SSEParser) Feed(chunk []byte) []SSEEvent {
	if len(chunk) == 0 {
		return nil
	}
	p.buffer = append(p.buffer, chunk...)
	var out []SSEEvent
	for {
		idx := bytes.IndexByte(p.buffer, '\n')
		if idx < 0 {
			break
		}
		raw := p.buffer[:idx]
		p.buffer = p.buffer[idx+1:]
		line := strings.TrimSuffix(string(raw), "\r")
		if line == "" {
			if ev, ok := p.flush(); ok {
				out = append(out, ev)
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			value := line[5:]
			value = strings.TrimPrefix(value, " ")
			p.dataLines = append(p.dataLines, value)
		}
	}
	return out
}

func (p *SSEParser) Close() []SSEEvent {
	if len(p.buffer) > 0 {
		line := strings.TrimSuffix(string(p.buffer), "\r")
		p.buffer = nil
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line[5:], " ")
			p.dataLines = append(p.dataLines, value)
		}
	}
	if ev, ok := p.flush(); ok {
		return []SSEEvent{ev}
	}
	return nil
}

func (p *SSEParser) flush() (SSEEvent, bool) {
	if len(p.dataLines) == 0 {
		return SSEEvent{}, false
	}
	payload := strings.Join(p.dataLines, "\n")
	p.dataLines = nil
	if payload == "[DONE]" {
		return SSEEvent{Done: true}, true
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		return SSEEvent{}, false
	}
	return SSEEvent{Data: data}, true
}

func SerializeEvent(event map[string]any) []byte {
	typ := toString(event["type"])
	if typ == "" {
		typ = "message"
	}
	raw, _ := json.Marshal(event)
	return []byte("event: " + typ + "\ndata: " + string(raw) + "\n\n")
}

func SerializeDone() []byte {
	return []byte("data: [DONE]\n\n")
}
