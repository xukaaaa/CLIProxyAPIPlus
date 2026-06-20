package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestAggregateClaudeSSEToMessage_TextAndUsageMerge(t *testing.T) {
	sse := []byte("event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"accounts/fireworks/models/kimi-k2p7-code","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n")

	out := AggregateClaudeSSEToMessage(sse)

	if got := gjson.GetBytes(out, "id").String(); got != "msg_1" {
		t.Fatalf("id = %q, want msg_1", got)
	}
	if got := gjson.GetBytes(out, "type").String(); got != "message" {
		t.Fatalf("type = %q, want message", got)
	}
	if got := gjson.GetBytes(out, "role").String(); got != "assistant" {
		t.Fatalf("role = %q, want assistant", got)
	}
	if got := gjson.GetBytes(out, "model").String(); got != "accounts/fireworks/models/kimi-k2p7-code" {
		t.Fatalf("model = %q", got)
	}
	if got := gjson.GetBytes(out, "content.0.type").String(); got != "text" {
		t.Fatalf("content.0.type = %q, want text", got)
	}
	if got := gjson.GetBytes(out, "content.0.text").String(); got != "Hello world" {
		t.Fatalf("content.0.text = %q, want 'Hello world'", got)
	}
	if got := gjson.GetBytes(out, "stop_reason").String(); got != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn", got)
	}
	if got := gjson.GetBytes(out, "usage.input_tokens").Int(); got != 10 {
		t.Fatalf("usage.input_tokens = %d, want 10", got)
	}
	if got := gjson.GetBytes(out, "usage.output_tokens").Int(); got != 5 {
		t.Fatalf("usage.output_tokens = %d, want 5", got)
	}
}

// Fireworks message_start.usage is always zero; the real input token count
// arrives in message_delta and must overwrite the zero from message_start.
func TestAggregateClaudeSSEToMessage_MessageDeltaInputTokensOverwriteZeroStart(t *testing.T) {
	sse := []byte("event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_fw","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":15,"output_tokens":7}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n")

	out := AggregateClaudeSSEToMessage(sse)

	if got := gjson.GetBytes(out, "usage.input_tokens").Int(); got != 15 {
		t.Fatalf("usage.input_tokens = %d, want 15 (real value from message_delta, not 0 from message_start); payload=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "usage.output_tokens").Int(); got != 7 {
		t.Fatalf("usage.output_tokens = %d, want 7; payload=%s", got, string(out))
	}
}

func TestAggregateClaudeSSEToMessage_ToolUse(t *testing.T) {
	sse := []byte("event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"SF\"}"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":8}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n")

	out := AggregateClaudeSSEToMessage(sse)

	if got := gjson.GetBytes(out, "content.0.type").String(); got != "tool_use" {
		t.Fatalf("content.0.type = %q, want tool_use", got)
	}
	if got := gjson.GetBytes(out, "content.0.id").String(); got != "toolu_1" {
		t.Fatalf("content.0.id = %q, want toolu_1", got)
	}
	if got := gjson.GetBytes(out, "content.0.name").String(); got != "get_weather" {
		t.Fatalf("content.0.name = %q, want get_weather", got)
	}
	if got := gjson.GetBytes(out, "content.0.input.city").String(); got != "SF" {
		t.Fatalf("content.0.input.city = %q, want SF; input=%s", got, gjson.GetBytes(out, "content.0.input").Raw)
	}
	if got := gjson.GetBytes(out, "stop_reason").String(); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use", got)
	}
}

func TestAggregateClaudeSSEToMessage_Thinking(t *testing.T) {
	sse := []byte("event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_3","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":2,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"let me think"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":4}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n")

	out := AggregateClaudeSSEToMessage(sse)

	if got := gjson.GetBytes(out, "content.0.type").String(); got != "thinking" {
		t.Fatalf("content.0.type = %q, want thinking", got)
	}
	if got := gjson.GetBytes(out, "content.0.thinking").String(); got != "let me think" {
		t.Fatalf("content.0.thinking = %q, want 'let me think'", got)
	}
	// Block at index 1 was never opened with content_block_start; deltas to an
	// unopened index must be ignored so content stays ordered and valid.
	if arr := gjson.GetBytes(out, "content").Array(); len(arr) != 1 {
		t.Fatalf("content len = %d, want 1 (unopened index ignored); content=%s", len(arr), gjson.GetBytes(out, "content").Raw)
	}
}
