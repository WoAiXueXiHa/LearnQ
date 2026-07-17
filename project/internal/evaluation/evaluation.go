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

type Judgment struct {
	ChunkID   string `json:"chunk_id"`
	Relevance int    `json:"relevance"`
}
type Case struct {
	ID             string     `json:"id"`
	Question       string     `json:"question"`
	RelevantChunks []Judgment `json:"relevant_chunks"`
	CitationText   string     `json:"citation_text"`
	CorrectAnswer  string     `json:"correct_answer"`
	Tags           []string   `json:"tags"`
	Difficulty     string     `json:"difficulty"`
}
type Scores struct {
	RecallAtK        float64 `json:"recall_at_k"`
	NDCG             float64 `json:"ndcg"`
	CitationCoverage float64 `json:"citation_coverage"`
}
type Report struct {
	Mode        string            `json:"mode"`
	DatasetSize int               `json:"dataset_size"`
	TopK        int               `json:"top_k"`
	Retrievers  map[string]Scores `json:"retrievers"`
	Runtime     time.Duration     `json:"runtime"`
}

type Retriever interface {
	Dense(context.Context, []float32, int) ([]rag.Hit, error)
	Sparse(context.Context, rag.SparseVector, int) ([]rag.Hit, error)
	Hybrid(context.Context, []float32, rag.SparseVector, int) ([]rag.Hit, error)
}

func ReadJSONL(reader io.Reader) ([]Case, error) {
	scanner := bufio.NewScanner(reader)
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
		cases = append(cases, item)
	}
	return cases, scanner.Err()
}

func Run(ctx context.Context, cases []Case, embedding model.EmbeddingModel, retriever Retriever, topK int, real bool) (Report, error) {
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
	if len(cases) > 0 {
		for name, value := range sums {
			n := float64(len(cases))
			report.Retrievers[name] = Scores{RecallAtK: value.recall / n, NDCG: value.ndcg / n, CitationCoverage: value.coverage / n}
		}
	}
	report.Runtime = time.Since(started)
	return report, nil
}

func hitIDs(hits []rag.Hit) []string {
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		if id, ok := hit.Payload["chunk_id"].(string); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

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
