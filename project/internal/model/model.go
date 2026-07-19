package model

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

type ChatRequest struct {
	Prompt string
	Skill  string
	Input  json.RawMessage
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
type EmbeddingModel interface {
	Embed(context.Context, []string) ([][]float32, error)
}

type Fake struct {
	// Fake 不是随意占位：相同输入产生确定性输出，可在无外部密钥时跑通完整验收链路。
	Dimension int
	Delay     time.Duration
	Failure   string
}

func (f Fake) Generate(ctx context.Context, req ChatRequest) (ChatResponse, error) {
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
	if req.Skill == "rag-answer" {
		var input struct {
			Question string `json:"question"`
			Evidence []struct {
				Source  string `json:"source"`
				Content string `json:"content"`
			} `json:"evidence"`
		}
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

func summarizeFakeInput(raw json.RawMessage) (string, string) {
	var input struct {
		Title           string `json:"title"`
		Topic           string `json:"topic"`
		Summary         string `json:"summary"`
		Question        string `json:"question"`
		DurationMinutes int    `json:"duration_minutes"`
		Modules         []struct {
			Category string `json:"category"`
			Content  string `json:"content"`
		} `json:"modules"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return compactJSON(raw, 240), "本次学习主题"
	}
	topic := strings.TrimSpace(input.Title)
	if topic == "" {
		topic = strings.TrimSpace(input.Topic)
	}
	if topic == "" {
		topic = "本次学习主题"
	}
	lines := []string{"- 主题：" + topic}
	if value := strings.TrimSpace(input.Summary); value != "" {
		lines = append(lines, "- 摘要："+value)
	}
	if value := strings.TrimSpace(input.Question); value != "" {
		lines = append(lines, "- 问题："+value)
	}
	if input.DurationMinutes > 0 {
		lines = append(lines, fmt.Sprintf("- 投入：%d 分钟", input.DurationMinutes))
	}
	for _, module := range input.Modules {
		content := strings.TrimSpace(module.Content)
		if content == "" {
			continue
		}
		category := strings.TrimSpace(module.Category)
		if category == "" {
			category = "未分类"
		}
		lines = append(lines, fmt.Sprintf("- 模块（%s）：%s", category, content))
	}
	return strings.Join(lines, "\n"), topic
}

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
