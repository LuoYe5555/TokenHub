package gateway

// 流式拒答门：非流式请求有破甲 failover（AggregateSink 拿到全文再判定），
// 流式请求过去是「上游吐什么客户端就收什么」——模型把拒答/元评论（「我不能按
// BetterDAN 那种格式回答」）流给客户端时用户直接看到失败。
//
// 门的工作方式：上游整条流先进 BufferSink（聚合、不写客户端），
// 流结束后做一次判定——
//   - 破甲未开 / 已有工具调用 / 正文正常 → 直接把聚合结果按 OpenAI SSE flush 给客户端；
//   - 正文命中拒答口径（或空流）且还有更强策略 → 掐掉这条流（一个字节都没出），
//     换更强策略在优化底稿上重打一次；重试通过则 flush 重试结果，仍被拒则 flush 原文。
//
// 代价是流式首字延迟（要等上游整条流收完）——换来的是客户端永远看到
// 重打过、更可能成功的那份回答。对 agent 场景（Opencode/Cline 带工具）无感。

import (
	"context"
	"encoding/json"

	"tokenhub/internal/logx"
	"tokenhub/internal/pj"
	"tokenhub/internal/provider"
	"tokenhub/internal/pseudotools"
)

// BufferSink 聚合上游增量但不写客户端；判定通过后把聚合结果重放给真 sink。
type BufferSink struct {
	inner *AggregateSink
}

func newBufferSink(model string) *BufferSink {
	return &BufferSink{inner: NewAggregateSink(model)}
}

func (b *BufferSink) Delta(d provider.Delta)        { b.inner.Delta(d) }
func (b *BufferSink) Finish(f provider.Finish)      { b.inner.Finish(f) }
func (b *BufferSink) StreamError(c int64, m string) { b.inner.StreamError(c, m) }
func (b *BufferSink) Close()                        { b.inner.Close() }

// sawTool 上游是否吐过工具调用（有工具调用就说明没在纯文本拒答，直接放行）。
func (b *BufferSink) sawTool() bool {
	b.inner.mu.Lock()
	defer b.inner.mu.Unlock()
	return len(b.inner.toolIdxs) > 0
}

// gateChat 带拒答门的流式调用。body=注入后的请求，prePJ=优化底稿（重试用），
// outSink=真客户端 sink，rec=外部 usageRecorder（gateChat 内部流量走自己的 recorder，
// 结束后把用量合并进 rec 供分享钥匙/用量日志计量）。
func (g *Gateway) gateChat(ctx context.Context, provName, model, rawModel string, body, prePJ []byte, outSink provider.Sink, rec *usageRecorder, pseudo bool) error {
	buf := newBufferSink(model)
	bufRec := &usageRecorder{inner: buf}
	err := g.pool.Chat(ctx, model, body, bufRec)
	if err != nil {
		g.mergeUsage(rec, bufRec)
		return err // 连接级失败：按普通错误路径走（客户端收 HTTP 错误）
	}
	raw, perr := buf.inner.Result()
	if perr != nil {
		g.mergeUsage(rec, bufRec)
		return perr
	}
	// 伪工具解析先于判定：思考流里也可能藏着 <tool_call>（ERNIE 惯犯），
	// 解析出的调用会落进 raw 的 tool_calls，下面靠 HasToolCalls 放行。
	if pseudo {
		raw = pseudotools.ApplyToResponse(raw)
	}
	txt := openaiContentText(raw)

	// 正文正常（或有工具调用）→ 放行
	if buf.sawTool() || pseudotools.HasToolCalls(raw) || (txt != "" && !pj.LooksRefused(txt)) {
		g.mergeUsage(rec, bufRec)
		flushOpenAIStream(outSink, raw)
		return nil
	}

	// 拒答/空流：还有更强策略就换策略重打一次（工具请求保持前置段 only）
	if stronger := pjNextStrategy(g.pjEffectiveStrategy(rawModel)); stronger != "" {
		logx.Infof("gateway", "破甲流式门：chat(%s) 回复被判拒答/空，换策略 %s 重试", model, stronger)
		retryBody := pj.ApplyStrategy(stronger, prePJ)
		if requestHasTools(prePJ) {
			if out, changed := pj.StripTemplate(retryBody); changed {
				retryBody = out
			}
			if plan := pj.PlanForBudget(stronger, g.pjEffectiveBudget()); plan.Front != "" {
				retryBody = pj.ApplyFrontOpts(plan.Front, retryBody, FrontLeanOpts(g))
			}
		}
		retryBuf := newBufferSink(model)
		retryRec := &usageRecorder{inner: retryBuf}
		if err2 := g.pool.Chat(ctx, model, retryBody, retryRec); err2 == nil {
			raw2, perr2 := retryBuf.inner.Result()
			if perr2 == nil {
				if pseudo {
					raw2 = pseudotools.ApplyToResponse(raw2)
				}
				txt2 := openaiContentText(raw2)
				if retryBuf.sawTool() || pseudotools.HasToolCalls(raw2) || (txt2 != "" && !pj.LooksRefused(txt2)) {
					logx.Infof("gateway", "破甲流式门：重试成功（策略 %s）", stronger)
					g.mergeUsage(rec, retryRec)
					flushOpenAIStream(outSink, raw2)
					return nil
				}
				logx.Infof("gateway", "破甲流式门：重试仍被拒，返回原结果")
			}
		} else {
			logx.Infof("gateway", "破甲流式门：重试调用失败，返回原结果: %v", err2)
		}
	}
	// 兜底：flush 原流内容（拒答文本也让客户端看到，不吞内容）
	g.mergeUsage(rec, bufRec)
	flushOpenAIStream(outSink, raw)
	return nil
}

// mergeUsage 把 bufRec 抓到的上游用量合并进外部 rec（存在才覆盖）。
func (g *Gateway) mergeUsage(rec, bufRec *usageRecorder) {
	if rec == nil {
		return
	}
	bufRec.mu.Lock()
	u := bufRec.usage
	bufRec.mu.Unlock()
	if u == nil || !u.Present {
		return
	}
	rec.mu.Lock()
	rec.usage = u
	rec.mu.Unlock()
}

// flushAnthropicStream 把一份聚合好的 OpenAI chat.completion 拆成增量写给
// AnthropicStreamSink（sink 内部负责转成 Anthropic message SSE）。
func flushAnthropicStream(sink provider.Sink, raw []byte) {
	var resp struct {
		Choices []struct {
			Message struct {
				Role             string `json:"role"`
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *provider.Usage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		sink.StreamError(500, "破甲流式门：聚合结果解析失败")
		return
	}
	for _, ch := range resp.Choices {
		m := ch.Message
		if m.ReasoningContent != "" {
			sink.Delta(provider.Delta{Reasoning: m.ReasoningContent})
		}
		if m.Content != "" {
			sink.Delta(provider.Delta{Role: m.Role, Content: m.Content})
		}
		for i, tc := range m.ToolCalls {
			sink.Delta(provider.Delta{Tool: &provider.ToolDelta{Index: i, ID: tc.ID, Name: tc.Function.Name, Args: tc.Function.Arguments}})
		}
	}
	sink.Finish(provider.Finish{Reason: finishReasonOrStop(resp.Choices[0].FinishReason), Usage: resp.Usage})
}

// gateChatAnthropic gateChat 的 Anthropic 版：缓冲聚合 → 判定 → 换策略重试
// → flushAnthropicStream 落给真客户端 sink。
func (g *Gateway) gateChatAnthropic(ctx context.Context, provName, model, rawModel string, body, prePJ []byte, outSink provider.Sink, rec *usageRecorder, pseudo bool) error {
	buf := newBufferSink(model)
	bufRec := &usageRecorder{inner: buf}
	err := g.pool.Chat(ctx, model, body, bufRec)
	if err != nil {
		g.mergeUsage(rec, bufRec)
		return err
	}
	raw, perr := buf.inner.Result()
	if perr != nil {
		g.mergeUsage(rec, bufRec)
		return perr
	}
	// 解析先于判定：思考流里也可能藏着 <tool_call>，解析出的调用落进 raw 的
	// tool_calls，下面靠 HasToolCalls 放行，不再误判「拒答/空」触发重试。
	if pseudo {
		raw = pseudotools.ApplyToResponse(raw)
	}
	txt := openaiContentText(raw)

	if buf.sawTool() || pseudotools.HasToolCalls(raw) || (txt != "" && !pj.LooksRefused(txt)) {
		g.mergeUsage(rec, bufRec)
		flushAnthropicStream(outSink, raw)
		return nil
	}

	if stronger := pjNextStrategy(g.pjEffectiveStrategy(rawModel)); stronger != "" {
		logx.Infof("gateway", "破甲流式门：messages(%s) 回复被判拒答/空，换策略 %s 重试", model, stronger)
		retryBody := pj.ApplyStrategy(stronger, prePJ)
		if requestHasTools(prePJ) {
			if out, changed := pj.StripTemplate(retryBody); changed {
				retryBody = out
			}
			if plan := pj.PlanForBudget(stronger, g.pjEffectiveBudget()); plan.Front != "" {
				retryBody = pj.ApplyFrontOpts(plan.Front, retryBody, FrontLeanOpts(g))
			}
		}
		retryBuf := newBufferSink(model)
		retryRec := &usageRecorder{inner: retryBuf}
		if err2 := g.pool.Chat(ctx, model, retryBody, retryRec); err2 == nil {
			raw2, perr2 := retryBuf.inner.Result()
			if perr2 == nil {
				if pseudo {
					raw2 = pseudotools.ApplyToResponse(raw2)
				}
				txt2 := openaiContentText(raw2)
				if retryBuf.sawTool() || pseudotools.HasToolCalls(raw2) || (txt2 != "" && !pj.LooksRefused(txt2)) {
					logx.Infof("gateway", "破甲流式门：重试成功（策略 %s）", stronger)
					g.mergeUsage(rec, retryRec)
					flushAnthropicStream(outSink, raw2)
					return nil
				}
				logx.Infof("gateway", "破甲流式门：重试仍被拒，返回原结果")
			}
		} else {
			logx.Infof("gateway", "破甲流式门：重试调用失败，返回原结果: %v", err2)
		}
	}
	g.mergeUsage(rec, bufRec)
	flushAnthropicStream(outSink, raw)
	return nil
}

// flushOpenAIStream 把一份聚合好的 chat.completion 拆成 OpenAI chunk SSE 写给真 sink：
// role+content/reasoning 一块、tool_calls 一块、finish 一块、[DONE] 收尾。
// 与 OpenAIStreamSink 的增量形状兼容（客户端按 chunk 累积）。
func flushOpenAIStream(sink provider.Sink, raw []byte) {
	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Role             string `json:"role"`
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *provider.Usage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		sink.StreamError(500, "破甲流式门：聚合结果解析失败")
		return
	}
	for _, ch := range resp.Choices {
		m := ch.Message
		if m.Content != "" || m.ReasoningContent != "" || (m.Role != "" && len(m.ToolCalls) == 0) {
			sink.Delta(provider.Delta{Role: m.Role, Content: m.Content, Reasoning: m.ReasoningContent})
		}
		for i, tc := range m.ToolCalls {
			sink.Delta(provider.Delta{Tool: &provider.ToolDelta{Index: i, ID: tc.ID, Name: tc.Function.Name, Args: tc.Function.Arguments}})
		}
	}
	u := resp.Usage
	sink.Finish(provider.Finish{Reason: finishReasonOrStop(resp.Choices[0].FinishReason), Usage: u})
}
