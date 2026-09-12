package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/medicore/backend/internal/llm"
)

// summarySchemaID 是编译 Schema 时使用的虚拟文件名。
const summarySchemaID = "preconsult_summary.json"

// maxSummaryAttempts 是结构化总结的总尝试次数（首次 + 重试 1 次，spec §7.2）。
const maxSummaryAttempts = 2

// retryInstruction 是校验失败后追加的纠正指令。
// 比首次提示更简短、更强硬——模型此时已经跑偏，再多解释只会更容易分心。
const retryInstruction = "你上一次的输出不是合法 JSON，或缺少必填字段。" +
	"请只输出一个 JSON 对象，不要任何解释文字，不要 Markdown 代码块标记。"

// SummaryValidator 用 JSON Schema 校验 LLM 的结构化输出。
type SummaryValidator struct {
	schema *jsonschema.Schema
}

// NewSummaryValidator 由 Schema 文本编译校验器。
//
// 编译失败必须在启动阶段暴露：Schema 写错却跑到线上才发现，
// 代价是整个降级路径（每次总结都降级，且没人知道为什么）。
func NewSummaryValidator(schemaJSON string) (*SummaryValidator, error) {
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(summarySchemaID, strings.NewReader(schemaJSON)); err != nil {
		return nil, fmt.Errorf("加载总结 Schema: %w", err)
	}
	schema, err := compiler.Compile(summarySchemaID)
	if err != nil {
		return nil, fmt.Errorf("编译总结 Schema: %w", err)
	}
	return &SummaryValidator{schema: schema}, nil
}

// Validate 校验 JSON 文本是否符合 Schema。
func (v *SummaryValidator) Validate(raw string) error {
	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return fmt.Errorf("输出不是合法 JSON: %w", err)
	}
	if err := v.schema.Validate(doc); err != nil {
		return fmt.Errorf("输出不符合 Schema: %w", err)
	}
	return nil
}

// SelfCheck 用空对象做一次反向验证，确认 Schema 真的会拒绝东西。
//
// 为什么需要它：`{}` 是**合法**的 JSON Schema，含义是"任何内容都通过"。
// 如果 Schema 文件被误改成空对象（或必填字段被删光），编译会成功、
// 每次校验都通过，于是降级路径永不触发，垃圾数据被当成结构化结果入库——
// 而且没有任何报错。这个自检把"静默失效"变成"启动即失败"。
func (v *SummaryValidator) SelfCheck() error {
	// 空对象必定缺少我们的必填字段，正常应当被拒绝
	if err := v.Validate(`{}`); err == nil {
		return errors.New("Schema 过于宽松：空对象也能通过校验，说明必填约束缺失，校验形同虚设")
	}
	return nil
}

// SummaryOutcome 是结构化总结的结果。
type SummaryOutcome struct {
	// Structured 是校验通过的 JSON 文本；降级时为空。
	Structured string
	// Attempts 是实际尝试次数。
	Attempts int
	// Fallback 为 true 表示未能得到合法的结构化输出，已降级为原始对话。
	Fallback bool
	// Reason 记录降级原因，写入报告的 error_message 供排查。
	Reason string
}

// summarize 调用上游生成结构化总结，失败时重试一次，仍失败则标记降级。
//
// 关键约定：本方法**不返回 error**。按 spec §7.2，结构化总结失败不是流程失败——
// 医生应该看到原始对话而不是一片空白。所有失败都通过 Fallback + Reason 表达。
func (s *PreconsultService) summarize(
	ctx context.Context, conversation []llm.Message,
) SummaryOutcome {
	outcome := SummaryOutcome{}

	// 复制一份再追加纠正指令，避免改动调用方持有的切片底层数组
	messages := make([]llm.Message, len(conversation))
	copy(messages, conversation)

	var lastErr error

	for attempt := 1; attempt <= maxSummaryAttempts; attempt++ {
		outcome.Attempts = attempt

		raw, err := s.client.Complete(ctx, llm.ChatRequest{
			SystemPrompt: s.summaryPrompt,
			Messages:     messages,
		})
		if err != nil {
			lastErr = err
			llmCallsTotal.WithLabelValues(llmPurposeSummary, classifyLLMError(err)).Inc()

			// 上下文已结束（超时/取消）时再重试没有意义
			if ctx.Err() != nil {
				break
			}
			continue
		}
		llmCallsTotal.WithLabelValues(llmPurposeSummary, llmResultSuccess).Inc()

		candidate, ok := extractJSON(raw)
		if ok {
			if err := s.validator.Validate(candidate); err == nil {
				outcome.Structured = candidate
				return outcome
			} else {
				lastErr = err
			}
		} else {
			lastErr = errors.New("输出中未找到合法 JSON")
		}

		// 把失败输出回喂，再补一句更严格的指令后重试
		messages = append([]llm.Message(nil), conversation...)
		messages = append(messages,
			llm.Message{Role: llm.RoleAssistant, Content: truncateRunes(raw, 500)},
			llm.Message{Role: llm.RoleUser, Content: retryInstruction},
		)
	}

	outcome.Fallback = true
	outcome.Reason = describeSummaryFailure(lastErr, outcome.Attempts)
	return outcome
}

// extractJSON 从 LLM 输出中提取 JSON 对象。
//
// 真实模型经常在 JSON 外面包一层说明文字或 ```json 代码块，
// 直接 json.Unmarshal 会失败。这里做三级尝试：
//  1. 整体就是 JSON
//  2. 剥掉 Markdown 代码块围栏
//  3. 取第一个 { 到最后一个 } 之间的内容
//
// 刻意不做更花哨的解析：如果输出已经混乱到这三步都救不回来，
// 正确做法是降级让医生看原始对话，而不是在解析器上继续堆代码。
func extractJSON(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}

	if json.Valid([]byte(trimmed)) {
		return trimmed, true
	}

	if unfenced, ok := stripCodeFence(trimmed); ok && json.Valid([]byte(unfenced)) {
		return unfenced, true
	}

	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		candidate := strings.TrimSpace(trimmed[start : end+1])
		if json.Valid([]byte(candidate)) {
			return candidate, true
		}
	}
	return "", false
}

// stripCodeFence 剥掉 ```json ... ``` 围栏。
func stripCodeFence(s string) (string, bool) {
	const fence = "```"
	if !strings.HasPrefix(s, fence) {
		return "", false
	}

	rest := s[len(fence):]
	// 跳过语言标注（```json）
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	} else {
		return "", false
	}

	end := strings.LastIndex(rest, fence)
	if end < 0 {
		return "", false
	}
	return strings.TrimSpace(rest[:end]), true
}

// describeSummaryFailure 生成对运维可读、对客户端不泄露细节的降级原因。
func describeSummaryFailure(err error, attempts int) string {
	switch {
	case err == nil:
		return "结构化总结未产出可用结果"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, llm.ErrTimeout):
		return "AI 服务响应超时，已降级为原始对话"
	case errors.Is(err, llm.ErrUpstream):
		return "AI 服务返回错误，已降级为原始对话"
	default:
		return fmt.Sprintf("结构化输出未通过校验（尝试 %d 次），已降级为原始对话", attempts)
	}
}

func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}
