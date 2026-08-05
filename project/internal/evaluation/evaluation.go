package evaluation

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/model"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/rag"
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
	ID             string     `json:"id"`
	Question       string     `json:"question"`
	RelevantChunks []Judgment `json:"relevant_chunks"`
	CitationText   string     `json:"citation_text"`
	CorrectAnswer  string     `json:"correct_answer"`
	Tags           []string   `json:"tags"`
	Difficulty     string     `json:"difficulty"`
}

// Scores 汇总单个检索器的一组指标，均为所有用例的平均值。
type Scores struct {
	RecallAtK        float64 `json:"recall_at_k"`
	NDCG             float64 `json:"ndcg"`
	CitationCoverage float64 `json:"citation_coverage"`
}

// Report 是一次评估运行的整体结果：Mode 区分 pipeline_test 与 retrieval_benchmark，
// Retrievers 按键名（dense-only / sparse-only / hybrid-rrf）分列得分。
type Report struct {
	Mode        string            `json:"mode"`
	DatasetSize int               `json:"dataset_size"`
	TopK        int               `json:"top_k"`
	Retrievers  map[string]Scores `json:"retrievers"`
	Runtime     time.Duration     `json:"runtime"`
}

// Retriever 抽象出三种检索路径，让同一数据集能对 dense / sparse / hybrid 横向对比。
type Retriever interface {
	Dense(context.Context, []float32, int) ([]rag.Hit, error)
	Sparse(context.Context, rag.SparseVector, int) ([]rag.Hit, error)
	Hybrid(context.Context, []float32, rag.SparseVector, int) ([]rag.Hit, error)
}

// ReadJSONL 逐行解析 JSONL 评估集：跳过空行，校验每个用例的必填字段
// 与 chunk_id 占位符，任一用例非法则整体失败并返回错误。
func ReadJSONL(reader io.Reader) ([]Case, error) {
	scanner := bufio.NewScanner(reader)
	// Scanner 默认单行上限 64 KiB，长文本用例会越界，这里放宽到 2 MiB。
	scanner.Buffer(make([]byte, 4096), 2<<20)
	var cases []Case
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var item Case
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return nil, err
		}
		if item.ID == "" || item.Question == "" || len(item.RelevantChunks) == 0 {
			return nil, fmt.Errorf("evaluation case misses id, question, or relevant_chunks")
		}
		for _, judgment := range item.RelevantChunks {
			// 模板样例常以 "replace-with-xxx" 作占位符，混入会算出无意义指标，直接拒绝。
			if strings.TrimSpace(judgment.ChunkID) == "" || strings.Contains(strings.ToLower(judgment.ChunkID), "replace-with") {
				return nil, fmt.Errorf("evaluation case %s contains an empty or placeholder chunk_id", item.ID)
			}
		}
		cases = append(cases, item)
	}
	return cases, scanner.Err()
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
	sums := map[string]*totals{"dense-only": {}, "sparse-only": {}, "hybrid-rrf": {}}
	for _, item := range cases {
		vectors, err := embedding.Embed(ctx, []string{item.Question})
		// 单个问题必须返回恰好一个向量，数量不符视为模型接口行为异常。
		if err != nil || len(vectors) != 1 {
			return report, fmt.Errorf("embed %s: %w", item.ID, err)
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
		// relevant 带等级供 Recall/NDCG 使用，required 是纯 ID 列表供引用覆盖率使用。
		relevant := map[string]int{}
		required := make([]string, len(item.RelevantChunks))
		for index, judgment := range item.RelevantChunks {
			relevant[judgment.ChunkID] = judgment.Relevance
			required[index] = judgment.ChunkID
		}
		for name, hits := range groups {
			ids := hitIDs(hits)
			sums[name].recall += rag.RecallAtK(ids, relevant, topK)
			sums[name].ndcg += rag.NDCGAtK(ids, relevant, topK)
			sums[name].coverage += rag.CitationCoverage(required, ids)
		}
	}
	// 用例数大于 0 才落均值，空数据集保留空得分映射而不是除零。
	if len(cases) > 0 {
		for name, value := range sums {
			n := float64(len(cases))
			report.Retrievers[name] = Scores{RecallAtK: value.recall / n, NDCG: value.ndcg / n, CitationCoverage: value.coverage / n}
		}
	}
	report.Runtime = time.Since(started)
	return report, nil
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

// Markdown 把评估结果渲染成 Markdown 表格，供人工阅读与存档。
func Markdown(report Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# RAG 离线评估\n\n模式：%s\n\n数据集：%d 条；Top K：%d；运行时间：%s\n\n", report.Mode, report.DatasetSize, report.TopK, report.Runtime)
	b.WriteString("| retriever | Recall@K | NDCG | 引用覆盖率 |\n|---|---:|---:|---:|\n")
	for _, name := range []string{"dense-only", "sparse-only", "hybrid-rrf"} {
		score := report.Retrievers[name]
		fmt.Fprintf(&b, "| %s | %.4f | %.4f | %.4f |\n", name, score.RecallAtK, score.NDCG, score.CitationCoverage)
	}
	return b.String()
}
