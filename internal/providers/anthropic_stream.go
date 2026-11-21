package providers

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// streamAnthropic translates an Anthropic Messages SSE stream into ChatChunks.
// It returns after message_stop (Done chunk), an error event, a read failure, or
// ctx cancellation. The caller closes ch and body.
func streamAnthropic(ctx context.Context, body io.Reader, cls Classifier, h http.Header, ch chan<- ChatChunk) {
	send := func(c ChatChunk) bool {
		select {
		case ch <- c:
			return true
		case <-ctx.Done():
			return false
		}
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), sseScannerBuf)

	var (
		id, model string
		usage     anthropicUsage
		finish    string
		toolIndex = map[int]int{} // anthropic content block index -> openai tool_call index
		nextTool  int
	)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "" {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				ID    string         `json:"id"`
				Model string         `json:"model"`
				Usage anthropicUsage `json:"usage"`
			} `json:"message"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Usage anthropicUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			id, model, usage = ev.Message.ID, ev.Message.Model, ev.Message.Usage
			if !send(ChatChunk{ID: id, Model: model}) {
				return
			}
		case "content_block_start":
			if ev.ContentBlock.Type != "tool_use" {
				continue
			}
			i := nextTool
			nextTool++
			toolIndex[ev.Index] = i
			if !send(ChatChunk{ID: id, Model: model, ToolCalls: []ToolCall{{
				Index: &i, ID: ev.ContentBlock.ID, Type: "function",
				Function: ToolCallFunction{Name: ev.ContentBlock.Name, Arguments: ""},
			}}}) {
				return
			}
		case "content_block_delta":
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text == "" {
					continue
				}
				if !send(ChatChunk{ID: id, Model: model, Content: ev.Delta.Text}) {
					return
				}
			case "input_json_delta":
				i, ok := toolIndex[ev.Index]
				if !ok || ev.Delta.PartialJSON == "" {
					continue
				}
				if !send(ChatChunk{ID: id, Model: model, ToolCalls: []ToolCall{{
					Index: &i, Function: ToolCallFunction{Arguments: ev.Delta.PartialJSON},
				}}}) {
					return
				}
			}
		case "message_delta":
			if ev.Usage.OutputTokens > 0 {
				usage.OutputTokens = ev.Usage.OutputTokens
			}
			if ev.Usage.InputTokens > 0 {
				usage.InputTokens = ev.Usage.InputTokens
			}
			if ev.Usage.CacheCreationInputTokens > 0 {
				usage.CacheCreationInputTokens = ev.Usage.CacheCreationInputTokens
			}
			if ev.Usage.CacheReadInputTokens > 0 {
				usage.CacheReadInputTokens = ev.Usage.CacheReadInputTokens
			}
			finish = mapStopReason(ev.Delta.StopReason)
			if finish != "" {
				if !send(ChatChunk{ID: id, Model: model, FinishReason: finish}) {
					return
				}
			}
		case "message_stop":
			u := usage.toUsage()
			if !send(ChatChunk{ID: id, Model: model, Usage: &u}) {
				return
			}
			send(ChatChunk{ID: id, Model: model, Done: true})
			return
		case "error":
			var e struct {
				Error struct {
					Type string `json:"type"`
				} `json:"error"`
			}
			_ = json.Unmarshal([]byte(data), &e)
			send(ChatChunk{Err: classifyWith(cls, anthropicStatusForType(e.Error.Type), h, []byte(data))})
			return
		}
	}
	if ctx.Err() != nil {
		return
	}
	code := "stream_interrupted"
	if scanner.Err() != nil {
		code = "stream_read_error"
	}
	send(ChatChunk{Err: &ProviderError{Class: ErrorClassTransient, Code: code, Message: "upstream stream ended unexpectedly"}})
}
