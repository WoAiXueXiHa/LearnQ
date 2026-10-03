package evaluation

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/WoAiXueXiHa/LearnQ/internal/rag"
)

// Judgment 是一条人工相关性标注：ChunkID 指向某块，Relevance 为相关等级
// （0 表示不相关，数值越大越相关，供 NDCG 区分）。
type Judgment struct {
	ChunkID   string `json:"chunk_id"`
	Relevance int    `json:"relevance"`
}

// Case 是一个评估用例：问题 + 相关块标注 + 期望引用文本与标准答案。
// Tags/Difficulty 供后续按维度分组分析预留，当前评估流程不消费。
type Case struct {
	DocumentID     uint64          `json:"document_id,omitempty"`
	ArticleSHA256  string          `json:"article_sha256,omitempty"`
	EvidencePoints []EvidencePoint `json:"evidence_points,omitempty"`
	ID             string          `json:"id"`
	Question       string          `json:"question"`
	RelevantChunks []Judgment      `json:"relevant_chunks"`
	CitationText   string          `json:"citation_text"`
	CorrectAnswer  string          `json:"correct_answer"`
	Tags           []string        `json:"tags"`
	Difficulty     string          `json:"difficulty"`
}

// RetrieverNames 固定报告与指标的输出顺序；map 迭代无序，顺序一变历史报告就无法逐次比对。
var RetrieverNames = []string{"dense-only", "sparse-only", "hybrid-rrf"}

// Scores 汇总单个检索器的一组检索指标，均为所有用例的平均值。
type Scores struct {
	RecallAtK         float64 `json:"recall_at_k"`
	NDCG              float64 `json:"ndcg"`
	RetrievalCoverage float64 `json:"retrieval_coverage"`
}

// Candidate 是一条进入 Top K 的候选：既保留身份与来源定位，也标记它是否属于人工标注。
// 只留均值无法说明“漏了哪一步”，逐例候选才是失败归因的依据。
type Candidate struct {
	Rank       int    `json:"rank"`
	ChunkID    string `json:"chunk_id"`
	DocumentID uint64 `json:"document_id"`
	Title      string `json:"title"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	Relevant   bool   `json:"relevant"`
	Excerpt    string `json:"excerpt"`
}

// CaseResult 逐例保存三路检索的候选与标注命中情况；两个 map 均以 RetrieverNames 为键。
type CaseResult struct {
	EvidenceByRetriever map[string][]EvidenceResult `json:"evidence_by_retriever,omitempty"`
	ID                  string                      `json:"id"`
	Question            string                      `json:"question"`
	Judged              []string                    `json:"judged_chunks"`
	Candidates          map[string][]Candidate      `json:"candidates"`
	HitByRetriever      map[string]bool             `json:"hit_by_retriever"`
}

// Report 是一次评估运行的整体结果：Mode 区分 pipeline_test 与 retrieval_benchmark，
// Retrievers 按键名分列得分，Cases 保留逐例证据。EmbeddingModel/Collection/DatasetHash
// 属于运行环境元数据，由调用方在渲染报告前补齐，Run 自身不读取配置。
type Report struct {
	Mode           string            `json:"mode"`
	DatasetSize    int               `json:"dataset_size"`
	TopK           int               `json:"top_k"`
	Retrievers     map[string]Scores `json:"retrievers"`
	Cases          []CaseResult      `json:"cases"`
	Runtime        time.Duration     `json:"runtime"`
	EmbeddingModel string            `json:"embedding_model"`
	Collection     string            `json:"collection"`
	DatasetHash    string            `json:"dataset_hash"`
	ResolvedHash   string            `json:"resolved_hash"`
	Indexes        []IndexMetadata   `json:"indexes,omitempty"`
}

// IndexMetadata records the actual immutable candidate scope, rather than
// guessing the chunking strategy from the current process configuration.
type IndexMetadata struct {
	DocumentID    uint64 `json:"document_id"`
	IndexID       uint64 `json:"index_id"`
	ArticleSHA256 string `json:"article_sha256"`
	IndexVersion  string `json:"index_version"`
	ChunkVersion  string `json:"chunk_version"`
	Dimension     int    `json:"dimension"`
}

// Retriever 抽象出三种检索路径，让同一数据集能对 dense / sparse / hybrid 横向对比。
type Retriever interface {
	Dense(context.Context, []float32, int) ([]rag.Hit, error)
	Sparse(context.Context, rag.SparseVector, int) ([]rag.Hit, error)
	Hybrid(context.Context, []float32, rag.SparseVector, int) ([]rag.Hit, error)
}

// CitationResolver resolves a citation snippet against the currently ready
// indexed chunks. It returns every chunk id whose content contains the snippet.
type CitationResolver func(context.Context, string) ([]string, error)

// ReadJSONL 逐行解析 JSONL 评估集：跳过空行，校验每个用例的必填字段
// 与 chunk_id 占位符，任一用例非法则整体失败并返回错误。
func ReadJSONL(reader io.Reader) ([]Case, error) {
	scanner := bufio.NewScanner(reader)
	// Scanner 默认单行上限 64 KiB，长文本用例会越界，这里放宽到 2 MiB。
	scanner.Buffer(make([]byte, 4096), 2<<20)
	var cases []Case
	seenIDs := map[string]bool{}
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var item Case
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return nil, err
		}
		item.ID = strings.TrimSpace(item.ID)
		item.Question = strings.TrimSpace(item.Question)
		item.CitationText = strings.TrimSpace(item.CitationText)
		if item.ID == "" || item.Question == "" {
			return nil, fmt.Errorf("evaluation case misses id or question")
		}
		if seenIDs[item.ID] {
			return nil, fmt.Errorf("duplicate evaluation case id %s", item.ID)
		}
		seenIDs[item.ID] = true
		if len(item.EvidencePoints) > 0 {
			if len(item.RelevantChunks) > 0 || item.CitationText != "" || item.DocumentID == 0 || len(item.ArticleSHA256) != 64 {
				return nil, fmt.Errorf("evaluation case %s has invalid evidence annotation", item.ID)
			}
			for _, p := range item.EvidencePoints {
				if strings.TrimSpace(p.Point) == "" || len(p.Lines) == 0 || len(p.ChunkIDs) > 0 || len(p.Alternatives) > 0 || len(p.Spans) > 0 {
					return nil, fmt.Errorf("evaluation case %s invalid evidence point", item.ID)
				}
			}
		}
		if len(item.RelevantChunks) == 0 && item.CitationText == "" && len(item.EvidencePoints) == 0 {
			return nil, fmt.Errorf("evaluation case %s misses relevant_chunks or citation_text", item.ID)
		}
		// 两种标注同时出现时 citation_text 会被静默忽略，等于作者以为标注生效而实际没有；
		// 宁可让数据集构建失败，也不接受一份无法判断意图的标注。
		if len(item.RelevantChunks) > 0 && item.CitationText != "" {
			return nil, fmt.Errorf("evaluation case %s sets both relevant_chunks and citation_text", item.ID)
		}
		if len(item.RelevantChunks) > 0 {
			if err := validateJudgments(item.ID, item.RelevantChunks); err != nil {
				return nil, err
			}
		}
		cases = append(cases, item)
	}
	return cases, scanner.Err()
}

// ResolveCitationText turns citation_text-only cases into relevant_chunks by
// searching ready document chunks. Missing or ambiguous snippets fail the whole
// dataset so metrics cannot be produced from accidental matches.
func ResolveCitationText(ctx context.Context, cases []Case, resolver CitationResolver) ([]Case, error) {
	if resolver == nil {
		return nil, errors.New("citation resolver is required")
	}
	resolved := make([]Case, len(cases))
	copy(resolved, cases)
	for index := range resolved {
		if len(resolved[index].RelevantChunks) > 0 || len(resolved[index].EvidencePoints) > 0 {
			continue
		}
		snippet := strings.TrimSpace(resolved[index].CitationText)
		if snippet == "" {
			return nil, fmt.Errorf("evaluation case %s misses citation_text", resolved[index].ID)
		}
		chunkIDs, err := resolver(ctx, snippet)
		if err != nil {
			return nil, fmt.Errorf("resolve citation_text for case %s: %w", resolved[index].ID, err)
		}
		switch len(chunkIDs) {
		case 0:
			return nil, fmt.Errorf("evaluation case %s citation_text matched no ready chunk", resolved[index].ID)
		case 1:
			resolved[index].RelevantChunks = []Judgment{{ChunkID: chunkIDs[0], Relevance: 3}}
		default:
			// 报出命中的块 ID：歧义多半来自切块重叠窗口，作者需要知道到底是哪两块在争同一条标注。
			return nil, fmt.Errorf("evaluation case %s citation_text is ambiguous: matched %d ready chunks (%s)",
				resolved[index].ID, len(chunkIDs), strings.Join(chunkIDs, ", "))
		}
	}
	return resolved, nil
}

func validateJudgments(caseID string, judgments []Judgment) error {
	seenChunks := make(map[string]struct{}, len(judgments))
	positive := 0
	for _, judgment := range judgments {
		chunkID := strings.TrimSpace(judgment.ChunkID)
		if chunkID == "" || strings.Contains(strings.ToLower(chunkID), "replace-with") {
			return fmt.Errorf("evaluation case %s contains an empty or placeholder chunk_id", caseID)
		}
		if judgment.Relevance < 0 {
			return fmt.Errorf("evaluation case %s contains negative relevance", caseID)
		}
		if _, duplicate := seenChunks[chunkID]; duplicate {
			return fmt.Errorf("evaluation case %s contains duplicate chunk_id %s", caseID, chunkID)
		}
		seenChunks[chunkID] = struct{}{}
		if judgment.Relevance > 0 {
			positive++
		}
	}
	if positive == 0 {
		return fmt.Errorf("evaluation case %s has no positively relevant chunk", caseID)
	}
	return nil
}

// Run 对全部用例跑三种检索路径并累计三组指标，最后按用例数取平均。
// 任一用例的 embedding 或检索失败都会提前返回部分 Report 与错误；
// real 为 true 时结果标记为 retrieval_benchmark，否则为 pipeline_test。
func Run(ctx context.Context, cases []Case, embedding model.EmbeddingModel, retriever Retriever, topK int, real bool) (Report, error) {
	// 同一数据集分别跑 dense、sparse、hybrid，指标差异才能归因于检索器而非输入变化。
	// fake 模式定位为管线测试；只有真实 embedding 的结果才标记为检索基准。
	started := time.Now()
	mode := "pipeline_test"
	if real {
		mode = "retrieval_benchmark"
	}
	report := Report{Mode: mode, DatasetSize: len(cases), TopK: topK, Retrievers: map[string]Scores{}}
	type totals struct{ recall, ndcg, coverage float64 }
	sums := map[string]*totals{}
	for _, name := range RetrieverNames {
		sums[name] = &totals{}
	}
	for _, item := range cases {
		vectors, err := embedding.Embed(ctx, []string{item.Question})
		if err != nil {
			return report, fmt.Errorf("embed %s: %w", item.ID, err)
		}
		// 单个问题必须返回恰好一个向量，数量不符视为模型接口行为异常，单独报错避免 nil 被格式化。
		if len(vectors) != 1 {
			return report, fmt.Errorf("embed %s: expected 1 vector, got %d", item.ID, len(vectors))
		}
		sparse := rag.Sparse(item.Question)
		groups := map[string][]rag.Hit{}
		if groups["dense-only"], err = retriever.Dense(ctx, vectors[0], topK); err != nil {
			return report, err
		}
		if groups["sparse-only"], err = retriever.Sparse(ctx, sparse, topK); err != nil {
			return report, err
		}
		if groups["hybrid-rrf"], err = retriever.Hybrid(ctx, vectors[0], sparse, topK); err != nil {
			return report, err
		}
		// relevant 带等级供 Recall/NDCG 使用，required 是纯 ID 列表供检索覆盖率使用。
		relevant := map[string]int{}
		var required []string
		for _, judgment := range item.RelevantChunks {
			relevant[judgment.ChunkID] = judgment.Relevance
			if judgment.Relevance > 0 {
				required = append(required, judgment.ChunkID)
			}
		}
		caseResult := CaseResult{
			ID: item.ID, Question: item.Question, Judged: required,
			Candidates: map[string][]Candidate{}, HitByRetriever: map[string]bool{}, EvidenceByRetriever: map[string][]EvidenceResult{},
		}
		for name, hits := range groups {
			ids := hitIDs(hits)
			retrieved := map[string]bool{}
			for _, id := range ids {
				retrieved[id] = true
			}
			for _, p := range item.EvidencePoints {
				e := EvidenceResult{Point: p.Point, ChunkIDs: p.ChunkIDs, Lines: p.Lines, DocumentID: item.DocumentID, ArticleSHA256: item.ArticleSHA256}
				for _, span := range p.Spans {
					e.Excerpts = append(e.Excerpts, excerpt(span.Text, 160))
				}
				e.Covered = evidenceCovered(p, retrieved)
				caseResult.EvidenceByRetriever[name] = append(caseResult.EvidenceByRetriever[name], e)
			}
			sums[name].recall += rag.RecallAtK(ids, relevant, topK)
			sums[name].ndcg += rag.NDCGAtK(ids, relevant, topK)
			sums[name].coverage += rag.RetrievalCoverage(required, ids)
			candidates := make([]Candidate, len(hits))
			for index, hit := range hits {
				// 逐条候选按原始返回顺序保留，因此这里不经过 hitIDs 的过滤：
				// 缺 chunk_id 的脏 payload 虽然不计入指标，但必须出现在报告里才看得见。
				candidates[index] = candidateOf(hit, index+1, relevant)
				if candidates[index].Relevant {
					caseResult.HitByRetriever[name] = true
				}
			}
			caseResult.Candidates[name] = candidates
		}
		report.Cases = append(report.Cases, caseResult)
	}
	// 用例数大于 0 才落均值，空数据集保留空得分映射而不是除零。
	if len(cases) > 0 {
		for name, value := range sums {
			n := float64(len(cases))
			report.Retrievers[name] = Scores{RecallAtK: value.recall / n, NDCG: value.ndcg / n, RetrievalCoverage: value.coverage / n}
		}
	}
	report.Runtime = time.Since(started)
	return report, nil
}

// candidateOf 把一条命中还原成可读候选：payload 由 Qdrant 返回，缺失字段按零值处理，
// 使报告在索引元数据不全时仍能生成而不是中断整次评估。
func candidateOf(hit rag.Hit, rank int, relevant map[string]int) Candidate {
	id, _ := hit.Payload["chunk_id"].(string)
	return Candidate{
		Rank: rank, ChunkID: id, Relevant: relevant[id] > 0,
		DocumentID: payloadUint(hit.Payload["document_id"]),
		Title:      payloadString(hit.Payload["title"]),
		StartLine:  int(payloadUint(hit.Payload["start_line"])),
		EndLine:    int(payloadUint(hit.Payload["end_line"])),
		Excerpt:    excerpt(payloadString(hit.Payload["summary"]), 80),
	}
}

// payloadUint 读取 payload 里的无符号整数。JSON 解码后的数字是 float64，
// 而单元测试桩可能直接塞原生整数，两种都接受，其余类型按缺失处理。
func payloadUint(value any) uint64 {
	switch typed := value.(type) {
	case float64:
		if typed > 0 {
			return uint64(typed)
		}
	case uint64:
		return typed
	case int:
		if typed > 0 {
			return uint64(typed)
		}
	case int64:
		if typed > 0 {
			return uint64(typed)
		}
	}
	return 0
}

// payloadString 读取 payload 里的字符串，缺失或类型不符时返回空串。
func payloadString(value any) string {
	text, _ := value.(string)
	return text
}

// excerpt 折叠换行与连续空白并按 rune 截断，避免摘录破坏 Markdown 表格的行结构。
func excerpt(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit])) + "…"
}

// hitIDs 从命中的 payload 里提取 chunk_id；类型断言失败（payload 缺失或类型不符）
// 的命中直接跳过，避免脏数据进入指标计算。
func hitIDs(hits []rag.Hit) []string {
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		if id, ok := hit.Payload["chunk_id"].(string); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// Markdown 把评估结果渲染成 Markdown：先给三路检索的均值汇总，再逐例列出候选、
// 标注命中与来源定位。均值只用于横向比较，逐例记录才说明命中或漏证据发生在哪一步。
func Markdown(report Report) string {
	var b strings.Builder
	b.WriteString("# RAG 离线评估\n\n")
	fmt.Fprintf(&b, "模式：%s（%s）\n\n", report.Mode, modeNote(report.Mode))
	fmt.Fprintf(&b, "数据集：%d 条；Top K：%d；运行时间：%s\n\n", report.DatasetSize, report.TopK, report.Runtime)
	if report.EmbeddingModel != "" || report.Collection != "" || report.DatasetHash != "" {
		fmt.Fprintf(&b, "Embedding 模型：%s；collection：%s；数据集哈希：%s\n\n",
			orDash(report.EmbeddingModel), orDash(report.Collection), orDash(report.DatasetHash))
	}
	if report.ResolvedHash != "" {
		fmt.Fprintf(&b, "解析后标注哈希：%s\n\n", report.ResolvedHash)
	}
	b.WriteString("跨度覆盖按持久化原文 UTF-8 字节区间校验，清洗后的检索文本不参与原文定位。\n\n")
	if len(report.Indexes) == 0 {
		b.WriteString("索引版本：未提供；不能由当前配置推断历史切块规则。\n\n")
	} else {
		b.WriteString("候选范围的文章与索引版本（包含本次检索可见的全部文档）：\n\n```json\n")
		metadata, _ := json.MarshalIndent(report.Indexes, "", "  ")
		b.Write(metadata)
		b.WriteString("\n```\n\n")
	}
	b.WriteString("## 汇总（全部用例平均）\n\n")
	b.WriteString("| retriever | Recall@K | NDCG | 检索覆盖率 | 无关候选 |\n|---|---:|---:|---:|---:|\n")
	for _, name := range RetrieverNames {
		score := report.Retrievers[name]
		fmt.Fprintf(&b, "| %s | %.4f | %.4f | %.4f | %d |\n",
			name, score.RecallAtK, score.NDCG, score.RetrievalCoverage, unrelatedCandidates(report.Cases, name))
	}
	b.WriteString("\n> 均值只用于三路检索横向比较；Recall@K/NDCG 衡量标注块是否进入 Top K，不代表回答正确率。\n\n")
	if len(report.Cases) == 0 {
		return b.String()
	}
	b.WriteString("## 逐例结果\n\n")
	for _, item := range report.Cases {
		b.WriteString(caseMarkdown(item))
	}
	return b.String()
}

// caseMarkdown 渲染单条用例：先给标注块与三路命中结论，再并列候选明细。
// 无关候选同样保留，否则无法判断漏证据是排序问题还是候选集根本没覆盖。
func caseMarkdown(item CaseResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s\n\n问题：%s\n\n", item.ID, item.Question)
	fmt.Fprintf(&b, "人工标注相关块：%s\n\n", orDash(strings.Join(codeList(item.Judged), "、")))
	verdicts := make([]string, 0, len(RetrieverNames))
	for _, name := range RetrieverNames {
		verdicts = append(verdicts, fmt.Sprintf("%s %s", name, hitMark(item.HitByRetriever[name])))
	}
	fmt.Fprintf(&b, "命中标注块：%s\n\n", strings.Join(verdicts, " ｜ "))
	total := 0
	for _, name := range RetrieverNames {
		total += len(item.Candidates[name])
	}

	for _, name := range RetrieverNames {
		points := item.EvidenceByRetriever[name]
		if len(points) == 0 {
			continue
		}
		covered := 0
		for _, p := range points {
			if p.Covered {
				covered++
			}
		}
		fmt.Fprintf(&b, "%s 证据点覆盖：%d/%d\n\n", name, covered, len(points))
		for _, p := range points {
			fmt.Fprintf(&b, "- %s %s（支持块：%s）\n", hitMark(p.Covered), cell(p.Point), strings.Join(codeList(p.ChunkIDs), "、"))
			fmt.Fprintf(&b, "  原文 #%d；SHA-256 %s；可接受行区间 %v\n", p.DocumentID, p.ArticleSHA256, p.Lines)
			for _, text := range p.Excerpts {
				fmt.Fprintf(&b, "  原文摘录：%s\n", cell(text))
			}
		}
		b.WriteString("\n")
	}
	if total == 0 {
		b.WriteString("本次召回没有任何候选。\n\n")
		return b.String()
	}
	b.WriteString("| retriever | rank | 相关 | 来源 | 行区间 | 块 ID | 摘录 |\n|---|---:|---|---|---|---|---|\n")
	for _, name := range RetrieverNames {
		for _, candidate := range item.Candidates[name] {
			fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s | %s |\n",
				name, candidate.Rank, hitMark(candidate.Relevant), sourceCell(candidate),
				lineRange(candidate), orDash(codeOrDash(candidate.ChunkID)), cell(candidate.Excerpt))
		}
	}
	b.WriteString("\n")
	return b.String()
}

// unrelatedCandidates 统计某检索器在所有用例的 Top K 里出现的非标注块数量。
func unrelatedCandidates(cases []CaseResult, retriever string) int {
	count := 0
	for _, item := range cases {
		for _, candidate := range item.Candidates[retriever] {
			if !candidate.Relevant {
				count++
			}
		}
	}
	return count
}

// modeNote 说明该模式下指标能代表什么：Fake 模型只验证管线连通，不能当作检索质量。
func modeNote(mode string) string {
	switch mode {
	case "retrieval_benchmark":
		return "真实 embedding，可作为检索基准"
	case "pipeline_test":
		return "Fake 模型，仅验证管线连通，不代表真实检索质量"
	default:
		return "未知模式，请核对评估配置"
	}
}

func hitMark(hit bool) string {
	if hit {
		return "✅"
	}
	return "❌"
}

func sourceCell(candidate Candidate) string {
	source := "—"
	if candidate.DocumentID > 0 {
		source = fmt.Sprintf("#%d", candidate.DocumentID)
	}
	if title := excerpt(candidate.Title, 24); title != "" {
		source += "《" + title + "》"
	}
	return source
}

func lineRange(candidate Candidate) string {
	if candidate.StartLine <= 0 {
		return "—"
	}
	return fmt.Sprintf("%d-%d", candidate.StartLine, candidate.EndLine)
}

func codeList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, "`"+value+"`")
	}
	return out
}

func codeOrDash(value string) string {
	if value == "" {
		return "—"
	}
	return "`" + value + "`"
}

func orDash(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

// cell 转义表格单元格：竖线会截断列、换行会截断行，两者都必须先处理。
func cell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	return strings.ReplaceAll(value, "\n", " ")
}
