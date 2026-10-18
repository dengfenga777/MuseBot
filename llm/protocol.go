package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/cohesion-org/deepseek-go"
	"github.com/sashabaranov/go-openai"
	"github.com/yincongcyincong/MuseBot/conf"
	"github.com/yincongcyincong/MuseBot/i18n"
	"github.com/yincongcyincong/MuseBot/logger"
	"github.com/yincongcyincong/MuseBot/metrics"
	"github.com/yincongcyincong/MuseBot/param"
)

type LLMToolCall struct {
	ID        string
	Name      string
	Type      string
	Arguments string
}

type LLMToolResult struct {
	ID      string
	Content string
}

type LLMToolState struct {
	Calls   []LLMToolCall
	Results []LLMToolResult
}

type LLMStreamChunk struct {
	Content   string
	ToolCalls []LLMToolCall
	Usage     int
	HasUsage  bool

	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
}

func retryLLMRequest[T any](l *LLM, operation func() (T, error)) (T, error) {
	var value T
	var err error

	times := conf.BaseConfInfo.LLMRetryTimes
	if times <= 0 {
		times = 1
	}
	for attempt := 0; attempt < times; attempt++ {
		value, err = operation()
		if err == nil {
			return value, nil
		}
		time.Sleep(time.Duration(conf.BaseConfInfo.LLMRetryInterval) * time.Millisecond)
	}
	return value, err
}

func observeLLMDuration(l *LLM, start time.Time) {
	metrics.APIRequestDuration.WithLabelValues(l.Model).Observe(time.Since(start).Seconds())
}

type llmSamplingOptions struct {
	FrequencyPenalty float32
	MaxTokens        int
	PresencePenalty  float32
	Temperature      float32
	TopLogProbs      int
	TopP             float32
	Stop             []string
	LogProbs         bool
}

func currentLLMSamplingOptions() llmSamplingOptions {
	if !conf.BaseConfInfo.LLMOptionParam {
		return llmSamplingOptions{}
	}

	return llmSamplingOptions{
		FrequencyPenalty: float32(conf.LLMConfInfo.FrequencyPenalty),
		MaxTokens:        conf.LLMConfInfo.MaxTokens,
		PresencePenalty:  float32(conf.LLMConfInfo.PresencePenalty),
		Temperature:      float32(conf.LLMConfInfo.Temperature),
		TopLogProbs:      conf.LLMConfInfo.TopLogProbs,
		TopP:             float32(conf.LLMConfInfo.TopP),
		Stop:             conf.LLMConfInfo.Stop,
		LogProbs:         conf.LLMConfInfo.LogProbs,
	}
}

func (o llmSamplingOptions) applyToOpenAI(request *openai.ChatCompletionRequest) {
	request.FrequencyPenalty = o.FrequencyPenalty
	request.MaxTokens = o.MaxTokens
	request.PresencePenalty = o.PresencePenalty
	request.Temperature = o.Temperature
	request.TopLogProbs = o.TopLogProbs
	request.TopP = o.TopP
	request.Stop = o.Stop
	request.LogProbs = o.LogProbs
}

func (o llmSamplingOptions) applyToDeepseek(request *deepseek.ChatCompletionRequest) {
	request.FrequencyPenalty = o.FrequencyPenalty
	request.MaxTokens = o.MaxTokens
	request.PresencePenalty = o.PresencePenalty
	request.Temperature = o.Temperature
	request.TopLogProbs = o.TopLogProbs
	request.TopP = o.TopP
	request.Stop = o.Stop
	request.LogProbs = o.LogProbs
}

func (o llmSamplingOptions) applyToDeepseekStream(request *deepseek.StreamChatCompletionRequest) {
	request.FrequencyPenalty = o.FrequencyPenalty
	request.MaxTokens = o.MaxTokens
	request.PresencePenalty = o.PresencePenalty
	request.Temperature = o.Temperature
	request.TopLogProbs = o.TopLogProbs
	request.TopP = o.TopP
	request.Stop = o.Stop
	request.LogProbs = o.LogProbs
}

func (s *LLMToolState) observe(call LLMToolCall) error {
	if call.Name != "" || len(s.Calls) == 0 {
		s.Calls = append(s.Calls, LLMToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Type:      call.Type,
			Arguments: call.Arguments,
		})
	} else {
		latest := &s.Calls[len(s.Calls)-1]
		if call.ID != "" {
			latest.ID = call.ID
		}
		if call.Type != "" {
			latest.Type = call.Type
		}
		latest.Arguments += call.Arguments
	}

	latest := &s.Calls[len(s.Calls)-1]
	var property map[string]interface{}
	if err := json.Unmarshal([]byte(latest.Arguments), &property); err != nil {
		return ToolsJsonErr
	}
	return nil
}

func (s *LLMToolState) execute(ctx context.Context, l *LLM) error {
	for _, call := range s.Calls {
		// a failed tool call is reported back to the llm instead of aborting the answer,
		// so it can retry with another source or answer with what it already has
		var property map[string]interface{}
		if err := json.Unmarshal([]byte(call.Arguments), &property); err != nil {
			logger.WarnCtx(l.Ctx, "tool arguments json unmarshal fail", "tool", call.Name, "args", call.Arguments, "err", err)
			s.Results = append(s.Results, LLMToolResult{ID: call.ID, Content: toolErrorContent(ToolsJsonErr)})
			continue
		}

		content, err := l.ExecMcpReq(ctx, call.Name, property)
		if err != nil {
			logger.ErrorCtx(ctx, "execute tool fail", "toolId", call.ID, "tool", call.Name, "err", err)
			content = toolErrorContent(err)
		}
		s.Results = append(s.Results, LLMToolResult{ID: call.ID, Content: content})
	}
	return nil
}

// sendToolStatus tell the user which kind of tool runs instead of showing the llm's chatter
func sendToolStatus(l *LLM, msgInfo *param.MsgInfo, calls []LLMToolCall) {
	if l.MessageChan == nil || msgInfo == nil {
		return
	}
	msgInfo.Intermediate = true
	msgInfo.Status = toolStatusText(calls)
	l.MessageChan <- msgInfo
}

func toolStatusText(calls []LLMToolCall) string {
	for _, call := range calls {
		name := strings.ToLower(call.Name)
		if strings.Contains(name, "search") {
			return i18n.GetMessage("tool_status_search", nil)
		}
	}
	for _, call := range calls {
		name := strings.ToLower(call.Name)
		if strings.Contains(name, "read") || strings.Contains(name, "fetch") || strings.Contains(name, "url") {
			return i18n.GetMessage("tool_status_read", nil)
		}
	}
	return i18n.GetMessage("tool_status_other", nil)
}

func toolErrorContent(err error) string {
	return "Tool call failed: " + err.Error() + ". Try another source or tool, or answer with the information you already have."
}

func (s *LLMToolState) observeComplete(ctx context.Context, l *LLM, calls []LLMToolCall) error {
	s.Calls = append(s.Calls, calls...)
	return s.execute(ctx, l)
}

func (s *LLMToolState) reset() {
	s.Calls = nil
	s.Results = nil
}

// toolPreambleHoldBytes is how much answer text is held back while tools are enabled.
// Text followed by a tool call is only "let me search" chatter and is never shown.
const toolPreambleHoldBytes = 300

func receiveLLMStream(ctx context.Context, l *LLM, next func() (LLMStreamChunk, error), tools *LLMToolState) (*param.MsgInfo, bool, error) {
	msgInfo := &param.MsgInfo{SendLen: FirstSendLen}
	hasTools := false

	holding := l.toolsEnabled()
	pending := ""
	flush := func() {
		holding = false
		if pending != "" {
			msgInfo = l.SendMsg(msgInfo, pending)
			pending = ""
		}
	}

	for {
		chunk, err := next()
		if errors.Is(err, io.EOF) {
			logger.InfoCtx(l.Ctx, "stream finished", "updateMsgID", l.MsgId)
			if hasTools {
				pending = ""
				sendToolStatus(l, msgInfo, tools.Calls)
			} else {
				flush()
			}
			if err := tools.execute(ctx, l); err != nil {
				return msgInfo, hasTools, err
			}
			return msgInfo, hasTools, nil
		}
		if err != nil {
			logger.WarnCtx(l.Ctx, "stream error", "updateMsgID", l.MsgId, "err", err)
			if !hasTools {
				flush()
			}
			return msgInfo, hasTools, err
		}

		for _, toolCall := range chunk.ToolCalls {
			hasTools = true
			if err := tools.observe(toolCall); err != nil {
				if errors.Is(err, ToolsJsonErr) {
					continue
				}
				logger.ErrorCtx(l.Ctx, "request tools call error", "updateMsgID", l.MsgId, "err", err)
				return msgInfo, hasTools, err
			}
		}

		if !hasTools {
			if chunk.Content != "" && l.FirstChunkTime.IsZero() {
				l.FirstChunkTime = time.Now()
			}
			if holding {
				pending += chunk.Content
				if len(pending) > toolPreambleHoldBytes {
					flush()
				}
			} else {
				msgInfo = l.SendMsg(msgInfo, chunk.Content)
			}
		}
		if chunk.HasUsage {
			l.Cs.Token += chunk.Usage
			l.PromptTokens += chunk.PromptTokens
			l.CompletionTokens += chunk.CompletionTokens
			l.CachedTokens += chunk.CachedTokens
		}
	}
}

func sendLLMStreamOutput(l *LLM, msgInfo *param.MsgInfo, hasTools bool) {
	if l.MessageChan == nil {
		return
	}
	hasContent := msgInfo != nil && len(strings.TrimRightFunc(msgInfo.Content, unicode.IsSpace)) > 0
	if !hasContent && (!hasTools || !conf.BaseConfInfo.SendMcpRes) {
		return
	}
	if msgInfo == nil {
		msgInfo = &param.MsgInfo{}
	}
	// tool rounds already showed their status before the tools ran
	if hasTools {
		return
	}
	if conf.BaseConfInfo.Powered != "" {
		msgInfo.Content += "\n\n" + conf.BaseConfInfo.Powered
	}
	if conf.BaseConfInfo.ShowStats {
		msgInfo.Content += "\n\n" + l.statsFooter()
	}
	l.MessageChan <- msgInfo
}
