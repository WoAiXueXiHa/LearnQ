package model

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// ChatRequest 是供应商无关的生成请求：Prompt 为系统提示词，Input 为用户输入，
// ResponseSchema 约束输出 JSON 结构。
type ChatRequest struct {
	MaxOutputTokens int
	Prompt          string
	Skill           string
	Input           json.RawMessage
	ResponseSchema  json.RawMessage
}
type ChatResponse struct {
	Content      string
	InputTokens  int
	OutputTokens int
	Model        string
}
type ChatModel interface {
	// 业务层只依赖最小接口，供应商协议、鉴权和错误分类留在适配器内部。
	Generate(context.Context, ChatRequest) (ChatResponse, error)
}

// EmbeddingModel 批量向量化文本；Fake 用哈希伪向量，real 走 OpenAI 兼容接口。
type EmbeddingModel interface {
	Embed(context.Context, []string) ([][]float32, error)
}

type Fake struct {
	// Fake 不是随意占位：相同输入产生确定性输出，可在无外部密钥时跑通完整验收链路。
	Dimension int
	Delay     time.Duration
	Failure   string
}

// Generate 按 Failure 注入故障、按 Skill 返回确定性 JSON 占位输出：
// "temporary" 模拟可重试错误，"invalid_json" 返回非法 JSON 以测试输出 Schema 校验链路。
func (f Fake) Generate(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	// Delay 模拟慢模型调用；select 让 ctx 取消（如任务超时）能立即中断等待。
	if f.Delay > 0 {
		select {
		case <-time.After(f.Delay):
		case <-ctx.Done():
			return ChatResponse{}, ctx.Err()
		}
	}
	switch f.Failure {
	case "temporary":
		return ChatResponse{}, errors.New("temporary model error")
	case "invalid_json":
		return ChatResponse{Content: "{"}, nil
	}
	if req.Skill == "authority-check" {
		return ChatResponse{Content: `{"judgment":"uncertain","explanation":"Fake 模式不核查事实，仅验证双来源管线。"}`, Model: "learnq-fake-authority-v1"}, nil
	}
	if req.Skill == "practice-feedback" {
		body, _ := json.Marshal(map[string]any{"items": []map[string]any{{"type": "expression", "judgment_status": "unable_to_judge", "explanation": "Fake 模式只验证反馈流程，不判断作答正确性。", "chunk_ids": []string{}, "image_ref_ids": []uint64{}}}})
		return ChatResponse{Content: string(body), Model: "learnq-fake-feedback-v1", InputTokens: len(req.Input) / 4, OutputTokens: len(body) / 4}, nil
	}
	if req.Skill == "practice-questions" {
		var input struct {
			Chunks []struct {
				ID      string `json:"id"`
				Content string `json:"content"`
			} `json:"chunks"`
			Images []struct {
				ID uint64 `json:"image_ref_id"`
			} `json:"images"`
		}
		if err := json.Unmarshal(req.Input, &input); err != nil {
			return ChatResponse{}, err
		}
		if len(input.Chunks) == 0 {
			return ChatResponse{}, errors.New("no article evidence")
		}
		questions := []map[string]any{}
		for j, prompt := range []string{"文章的核心问题是什么？", "核心机制如何按步骤运行？", "关键方案有哪些取舍？", "失败情形和边界如何处理？", "如何串联全文解释这个知识点？"} {
			chunk := input.Chunks[j%len(input.Chunks)]
			ids := []string{chunk.ID}
			for k, c := range input.Chunks {
				if k%5 == j && c.ID != chunk.ID {
					ids = append(ids, c.ID)
				}
			}
			imageIDs := []uint64{}
			for k, image := range input.Images {
				if k%5 == j {
					imageIDs = append(imageIDs, image.ID)
				}
			}
			points := []string{"演示要点：说明本题涉及的概念与步骤；请按文章人工核对。", "演示要点：补充适用条件和失败边界；此内容不是文章分析结果。"}
			items := []map[string]any{}
			for _, point := range points {
				items = append(items, map[string]any{"text": point, "chunk_ids": ids, "image_ref_ids": imageIDs})
			}
			questions = append(questions, map[string]any{"prompt": prompt, "knowledge_points": []string{"Fake 流程占位，需人工检查"}, "reference_points": points, "reference_items": items, "chunk_ids": ids, "image_ref_ids": imageIDs})
		}
		body, _ := json.Marshal(map[string]any{"questions": questions})
		return ChatResponse{Content: string(body), Model: "learnq-fake-questions-v2", InputTokens: len(req.Input) / 4, OutputTokens: len(body) / 4}, nil
	}
	if req.Skill == "agent-planner" {
		var input struct {
			Message      string         `json:"message"`
			AllowActions bool           `json:"allow_actions"`
			Context      map[string]any `json:"context"`
		}
		_ = json.Unmarshal(req.Input, &input)
		taskType := "knowledge_qa"
		if strings.Contains(input.Message, "复盘") || strings.Contains(input.Message, "学习") || strings.Contains(input.Message, "复习") {
			taskType = "learning_review"
		} else if strings.Contains(input.Message, "项目") || strings.Contains(input.Message, "面试") || strings.Contains(input.Message, "讲解") {
			taskType = "project_explanation"
		}
		steps := []map[string]any{}
		switch taskType {
		case "knowledge_qa":
			steps = append(steps, map[string]any{"id": 1, "purpose": "检索可核验知识", "tool": "rag_search", "args": map[string]any{"question": input.Message, "top_k": 5}})
		case "learning_review":
			steps = append(steps,
				map[string]any{"id": 1, "purpose": "查询相关学习历史", "tool": "study_history_search", "args": map[string]any{"query": input.Message, "limit": 5}},
				map[string]any{"id": 2, "purpose": "读取近七天学习统计", "tool": "weekly_stats", "args": map[string]any{}})
			if input.AllowActions {
				if reportID, ok := input.Context["report_id"]; ok {
					steps = append(steps, map[string]any{"id": 3, "purpose": "安排下一次复习", "tool": "review_task_create", "args": map[string]any{"report_id": reportID}})
				}
			}
		case "project_explanation":
			steps = append(steps,
				map[string]any{"id": 1, "purpose": "检索项目事实", "tool": "rag_search", "args": map[string]any{"question": input.Message, "top_k": 5}},
				map[string]any{"id": 2, "purpose": "查询相关学习记录", "tool": "study_history_search", "args": map[string]any{"query": input.Message, "limit": 5}})
		}
		content, _ := json.Marshal(map[string]any{"task_type": taskType, "intent": input.Message, "steps": steps})
		return ChatResponse{Content: string(content), Model: "learnq-fake-planner-v1", InputTokens: len(req.Input) / 4, OutputTokens: len(content) / 4}, nil
	}
	if req.Skill == "agent-synthesizer" {
		var input struct {
			Message      string `json:"message"`
			TaskType     string `json:"task_type"`
			Observations []struct {
				Tool      string `json:"tool"`
				Result    any    `json:"result"`
				Citations []struct {
					Source  string `json:"source"`
					Content string `json:"content"`
				} `json:"citations"`
			} `json:"observations"`
		}
		_ = json.Unmarshal(req.Input, &input)
		answer := "已根据学习事实整理出下一步建议。"
		for _, observation := range input.Observations {
			if len(observation.Citations) > 0 {
				citation := observation.Citations[0]
				answer = fmt.Sprintf("关于“%s”，可核验资料指出：%s [%s]", input.Message, strings.TrimSpace(citation.Content), citation.Source)
				break
			}
		}
		if input.TaskType == "project_explanation" && answer == "已根据学习事实整理出下一步建议。" {
			answer = "当前没有足够项目事实，无法核验具体实现细节。"
		}
		content, _ := json.Marshal(map[string]string{"answer": answer})
		return ChatResponse{Content: string(content), Model: "learnq-fake-synthesizer-v1", InputTokens: len(req.Input) / 4, OutputTokens: len(content) / 4}, nil
	}
	if req.Skill == "rag-answer" {
		var input struct {
			Question string `json:"question"`
			Evidence []struct {
				Source  string `json:"source"`
				Content string `json:"content"`
			} `json:"evidence"`
		}
		// RAG 回答必须携带可核验证据，缺失时拒绝生成而不是让模型编造引用。
		if err := json.Unmarshal(req.Input, &input); err != nil || len(input.Evidence) == 0 {
			return ChatResponse{}, errors.New("rag answer requires evidence")
		}
		answer := fmt.Sprintf("关于“%s”，可核验资料指出：%s [%s]", input.Question, strings.TrimSpace(input.Evidence[0].Content), input.Evidence[0].Source)
		content, _ := json.Marshal(map[string]string{"answer": answer})
		return ChatResponse{
			Content: string(content), InputTokens: len([]rune(req.Prompt)) / 4,
			OutputTokens: len(content) / 4, Model: "learnq-fake-chat-v1",
		}, nil
	}
	inputSummary, topic := summarizeFakeInput(req.Input)
	title, summary := "LearnQ 学习报告", "已根据输入事实生成确定性结果。"
	sections := []map[string]string{{"heading": "输入事实", "content": inputSummary}}
	switch req.Skill {
	case "daily-review":
		title = "每日学习复盘"
		summary = "复盘仅使用本次学习记录中的事实。"
		sections = append(sections,
			map[string]string{"heading": "理解检查", "content": fmt.Sprintf("- 不看笔记，用自己的话解释“%s”。\n- 说明一个容易出错的边界条件。", topic)},
			map[string]string{"heading": "面试追问", "content": fmt.Sprintf("为什么在“%s”中采用当前方案？如果依赖失败或并发增加，正确性如何保证？", topic)},
			map[string]string{"heading": "下一步", "content": fmt.Sprintf("围绕“%s”完成一次最小可运行验证，并记录输入、预期结果和实际结果。", topic)})
	case "algorithm-diagnosis":
		title = "算法学习诊断"
		summary = "从思路、边界和复杂度三个角度检查算法记录。"
		sections = append(sections,
			map[string]string{"heading": "边界案例", "content": "验证空输入、单元素、重复元素与极端规模。"},
			map[string]string{"heading": "练习", "content": "手写一次状态转移并说明时间、空间复杂度。"})
	case "interview-followup":
		title = "面试递进追问"
		summary = "追问从事实描述逐步进入原理和工程权衡。"
		sections = append(sections,
			map[string]string{"heading": "基础追问", "content": "请先说明问题、约束和采用的方案。"},
			map[string]string{"heading": "深入追问", "content": "如果依赖失败或并发增加，方案如何保持正确性？"})
	case "project-explanation":
		title = "项目讲解稿"
		summary = "按问题、方案、权衡与验证组织项目事实。"
		sections = append(sections,
			map[string]string{"heading": "方案与权衡", "content": "说明事实源、异步边界和一致性代价。"},
			map[string]string{"heading": "验证", "content": "用测试、指标和故障恢复证据支撑结论。"})
	case "weekly-plan":
		title = "一周学习计划"
		summary = "计划中的统计值来自 weekly_stats 工具结果。"
		sections = append(sections,
			map[string]string{"heading": "本周事实", "content": "解释 SQL 汇总的时长、模块分布和任务状态。"},
			map[string]string{"heading": "计划", "content": "基于已有记录安排复习、练习和项目验证。"})
	}
	content, _ := json.Marshal(map[string]any{"title": title, "summary": summary, "sections": sections})
	return ChatResponse{Content: string(content), InputTokens: len([]rune(req.Prompt)) / 4, OutputTokens: len(content) / 4, Model: "learnq-fake-chat-v1"}, nil
}

// compactJSON 把输入压缩为单行摘要并截断到 limit 字符：合法 JSON 经重新序列化
// 压成单行，超长内容截断后补省略号。
func compactJSON(raw json.RawMessage, limit int) string {
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "未提供输入"
	}
	var compact any
	if json.Unmarshal(raw, &compact) == nil {
		body, _ := json.Marshal(compact)
		value = string(body)
	}
	runes := []rune(value)
	if len(runes) > limit {
		value = string(runes[:limit]) + "…"
	}
	return value
}

// summarizeFakeInput 从输入 JSON 提取主题与可读事实列表供 Fake 报告引用；
// 兼容 {request:{...}} 包裹结构与顶层平铺两种形态。
func summarizeFakeInput(raw json.RawMessage) (string, string) {
	var input map[string]any
	if json.Unmarshal(raw, &input) != nil {
		return "输入无法解析。", "本次学习主题"
	}
	if request, ok := input["request"].(map[string]any); ok {
		for _, key := range []string{"title", "topic", "summary", "question", "duration_minutes", "modules"} {
			if _, exists := input[key]; !exists {
				input[key] = request[key]
			}
		}
	}
	text := func(key string) string {
		value, _ := input[key].(string)
		return strings.TrimSpace(value)
	}
	topic := text("title")
	if topic == "" {
		topic = text("topic")
	}
	if topic == "" {
		topic = "本次学习主题"
	}
	lines := []string{"- 主题：" + topic}
	if value := text("summary"); value != "" {
		lines = append(lines, "- 摘要："+value)
	}
	if value := text("question"); value != "" {
		lines = append(lines, "- 问题："+value)
	}
	if value, ok := input["duration_minutes"].(float64); ok && value > 0 {
		lines = append(lines, fmt.Sprintf("- 投入：%d 分钟", int(value)))
	}
	if modules, ok := input["modules"].([]any); ok {
		for _, item := range modules {
			switch module := item.(type) {
			case string:
				if value := strings.TrimSpace(module); value != "" {
					lines = append(lines, "- 模块："+value)
				}
			case map[string]any:
				content, _ := module["content"].(string)
				category, _ := module["category"].(string)
				content, category = strings.TrimSpace(content), strings.TrimSpace(category)
				if content != "" {
					if category == "" {
						category = "未分类"
					}
					lines = append(lines, fmt.Sprintf("- 模块（%s）：%s", category, content))
				}
			}
		}
	}
	if outputs, ok := input["agent_outputs"].(map[string]any); ok && len(outputs) > 0 {
		names := make([]string, 0, len(outputs))
		for name := range outputs {
			names = append(names, name)
		}
		sort.Strings(names)
		lines = append(lines, "- 已汇总 Agent："+strings.Join(names, "、"))
	}
	return strings.Join(lines, "\n"), topic
}

// Embed 生成确定性伪向量：token 经哈希散列到固定维度并带上符号累加，最后 L2 归一化。
// 不访问网络，仅用于 fake 模式跑通向量索引与检索链路。
func (f Fake) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	dim := f.Dimension
	if dim <= 0 {
		dim = 64
	}
	out := make([][]float32, len(texts))
	for i, text := range texts {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		// token 经哈希均匀散列到 dim 个维度；哈希第 5 字节决定符号，
		// 使不同 token 可在同一维度正负相抵、保留更多区分信息。
		v := make([]float32, dim)
		for _, token := range strings.Fields(strings.ToLower(text)) {
			sum := sha256.Sum256([]byte(token))
			idx := binary.LittleEndian.Uint32(sum[:4]) % uint32(dim)
			sign := float32(1)
			if sum[4]&1 == 1 {
				sign = -1
			}
			v[idx] += sign
		}
		var norm float64
		for _, x := range v {
			norm += float64(x * x)
		}
		// 全零向量（空文本）跳过归一化，避免除零。
		if norm > 0 {
			norm = math.Sqrt(norm)
			for j := range v {
				v[j] /= float32(norm)
			}
		}
		out[i] = v
	}
	return out, nil
}

// MarkdownFromJSON 把 Skill 输出的结构化 JSON 渲染为 Markdown 报告；
// 必填字段缺失即报错，拒绝把半成品写盘。
func MarkdownFromJSON(raw string) (string, error) {
	var v struct {
		Title    string `json:"title"`
		Summary  string `json:"summary"`
		Sections []struct {
			Heading string `json:"heading"`
			Content string `json:"content"`
		} `json:"sections"`
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return "", fmt.Errorf("decode structured output: %w", err)
	}
	if v.Title == "" || v.Summary == "" || len(v.Sections) == 0 {
		return "", errors.New("structured output is missing required fields")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s\n", v.Title, v.Summary)
	for _, section := range v.Sections {
		if section.Heading == "" || section.Content == "" {
			return "", errors.New("section is missing heading or content")
		}
		fmt.Fprintf(&b, "\n## %s\n\n%s\n", section.Heading, section.Content)
	}
	return b.String(), nil
}
