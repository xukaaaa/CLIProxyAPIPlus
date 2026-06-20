package helps

import (
	"bytes"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// AggregateClaudeSSEToMessage reconstructs a single Anthropic non-streaming
// "message" JSON object from an Anthropic-protocol Server-Sent Events stream
// (the shape returned by /v1/messages with stream=true).
//
// It is used by executors that force upstream streaming but need to return a
// non-streaming message to a claude-format client. Events are accumulated in a
// single pass: message_start (id/model/role/input usage), content_block_start /
// content_block_delta / content_block_stop (text, thinking, and tool_use
// blocks), and message_delta (stop_reason + output usage).
func AggregateClaudeSSEToMessage(data []byte) []byte {
	var (
		id             string
		model          string
		role           = "assistant"
		stopReason     string
		stopReasonSet  bool
		inputTokens    int64
		outputTokens   int64
		cacheCreation  int64
		cacheRead      int64
		hasInputUsage  bool
		hasOutputUsage bool
		blocks         = map[int]*claudeSSEBlock{}
	)

	for _, raw := range bytes.Split(data, []byte("\n")) {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || !gjson.ValidBytes(payload) {
			continue
		}
		root := gjson.ParseBytes(payload)
		switch root.Get("type").String() {
		case "message_start":
			message := root.Get("message")
			if message.Exists() {
				id = message.Get("id").String()
				model = message.Get("model").String()
				if r := strings.TrimSpace(message.Get("role").String()); r != "" {
					role = r
				}
				usage := message.Get("usage")
				if usage.Exists() {
					inputTokens = usage.Get("input_tokens").Int()
					cacheCreation = usage.Get("cache_creation_input_tokens").Int()
					cacheRead = usage.Get("cache_read_input_tokens").Int()
					hasInputUsage = true
				}
			}
		case "content_block_start":
			idx := int(root.Get("index").Int())
			block := root.Get("content_block")
			if !block.Exists() {
				continue
			}
			b := &claudeSSEBlock{index: idx, typ: block.Get("type").String()}
			if b.typ == "tool_use" {
				b.toolID = block.Get("id").String()
				b.toolName = block.Get("name").String()
			}
			blocks[idx] = b
		case "content_block_delta":
			idx := int(root.Get("index").Int())
			b, ok := blocks[idx]
			if !ok {
				continue
			}
			delta := root.Get("delta")
			if !delta.Exists() {
				continue
			}
			switch delta.Get("type").String() {
			case "text_delta":
				b.text.WriteString(delta.Get("text").String())
			case "thinking_delta":
				b.text.WriteString(delta.Get("thinking").String())
			case "input_json_delta":
				b.toolInput.WriteString(delta.Get("partial_json").String())
				b.hasToolInput = true
			}
		case "content_block_stop":
			// nothing to finalize beyond marking presence; block already in map
		case "message_delta":
			delta := root.Get("delta")
			if delta.Exists() {
				if sr := delta.Get("stop_reason"); sr.Exists() {
					stopReason = sr.String()
					stopReasonSet = true
				}
			}
			usage := root.Get("usage")
			if usage.Exists() {
				if v := usage.Get("output_tokens"); v.Exists() {
					outputTokens = v.Int()
					hasOutputUsage = true
				}
				if v := usage.Get("cache_creation_input_tokens"); v.Exists() {
					cacheCreation = v.Int()
				}
				if v := usage.Get("cache_read_input_tokens"); v.Exists() {
					cacheRead = v.Int()
				}
				if v := usage.Get("input_tokens"); v.Exists() {
					inputTokens = v.Int()
					hasInputUsage = true
				}
			}
		case "message_stop":
			// terminal, nothing to capture
		}
	}

	contentArr := []byte("[]")
	indices := make([]int, 0, len(blocks))
	for idx := range blocks {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	for _, idx := range indices {
		contentArr, _ = sjson.SetRawBytes(contentArr, "-1", blocks[idx].marshalJSON())
	}

	out := []byte("{}")
	out, _ = sjson.SetBytes(out, "id", id)
	out, _ = sjson.SetBytes(out, "type", "message")
	out, _ = sjson.SetBytes(out, "role", role)
	out, _ = sjson.SetBytes(out, "model", model)
	out, _ = sjson.SetRawBytes(out, "content", contentArr)
	if stopReasonSet {
		out, _ = sjson.SetBytes(out, "stop_reason", stopReason)
	} else {
		out, _ = sjson.SetBytes(out, "stop_reason", nil)
	}
	out, _ = sjson.SetBytes(out, "stop_sequence", nil)

	usage := []byte("{}")
	if hasInputUsage || hasOutputUsage {
		usage, _ = sjson.SetBytes(usage, "input_tokens", inputTokens)
		usage, _ = sjson.SetBytes(usage, "output_tokens", outputTokens)
		usage, _ = sjson.SetBytes(usage, "cache_creation_input_tokens", cacheCreation)
		usage, _ = sjson.SetBytes(usage, "cache_read_input_tokens", cacheRead)
	}
	out, _ = sjson.SetRawBytes(out, "usage", usage)

	return out
}

type claudeSSEBlock struct {
	index        int
	typ          string
	text         strings.Builder
	toolID       string
	toolName     string
	toolInput    strings.Builder
	hasToolInput bool
}

func (b *claudeSSEBlock) marshalJSON() []byte {
	switch b.typ {
	case "thinking":
		out := []byte(`{"type":"thinking","thinking":""}`)
		out, _ = sjson.SetBytes(out, "thinking", b.text.String())
		return out
	case "tool_use":
		out := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
		out, _ = sjson.SetBytes(out, "id", b.toolID)
		out, _ = sjson.SetBytes(out, "name", b.toolName)
		raw := b.toolInput.String()
		if b.hasToolInput && gjson.Valid(raw) {
			out, _ = sjson.SetRawBytes(out, "input", []byte(raw))
		} else {
			out, _ = sjson.SetBytes(out, "input", nil)
		}
		return out
	default: // "text" and any unknown -> text block
		out := []byte(`{"type":"text","text":""}`)
		if b.typ != "" && b.typ != "text" {
			out, _ = sjson.SetBytes(out, "type", b.typ)
		}
		out, _ = sjson.SetBytes(out, "text", b.text.String())
		return out
	}
}
