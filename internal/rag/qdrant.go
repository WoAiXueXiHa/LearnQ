package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Qdrant 是对 Qdrant HTTP API 的极简封装（无官方 Go SDK，全部走原始 HTTP）。
// BaseURL 为服务根地址，Collection 为集合名；Client 可注入以便测试替换。
type Qdrant struct {
	BaseURL, Collection string
	Client              *http.Client
}

// Hit 是检索命中的点：ID 为 Qdrant 点主键（稳定 ID 的 UUID 形式），
// Payload 携带 chunk_id 等元数据，评估与引用校验都从 Payload 还原块身份。
type Hit struct {
	ID      string         `json:"id"`
	Score   float64        `json:"score"`
	Payload map[string]any `json:"payload"`
}

// Point 是待写入的向量点；Dense/Sparse 标记 json:"-" 不直接序列化，
// Upsert 时手工组装为 Qdrant 的 named vectors 结构（dense 与 sparse 共存于一点）。
type Point struct {
	ID      string         `json:"id"`
	Dense   []float32      `json:"-"`
	Sparse  SparseVector   `json:"-"`
	Payload map[string]any `json:"payload"`
}

// EnsureCollection 幂等创建集合：dense 向量用 Cosine 距离，sparse 用命名向量。
// 集合已存在时逐一核对 dense 维度、距离与 sparse 定义，不一致直接报错要求重建。
func (q Qdrant) EnsureCollection(ctx context.Context, dimension int) error {
	// 创建冲突并不直接视为成功：继续读取现有集合并核对 dense 维度，
	// 防止更换 embedding 模型后把不兼容向量写进旧集合。
	body := map[string]any{"vectors": map[string]any{"dense": map[string]any{"size": dimension, "distance": "Cosine"}}, "sparse_vectors": map[string]any{"sparse": map[string]any{}}}
	err := q.put(ctx, "/collections/"+q.Collection, body)
	if err == nil {
		return nil
	}
	// 无 SDK 可用，只能从错误文本中识别 409 冲突；其余状态码原样返回。
	if !strings.Contains(err.Error(), "409") {
		return err
	}
	var existing struct {
		Result struct {
			Config struct {
				Params struct {
					Vectors map[string]struct {
						Size     int    `json:"size"`
						Distance string `json:"distance"`
					} `json:"vectors"`
					SparseVectors map[string]json.RawMessage `json:"sparse_vectors"`
				} `json:"params"`
			} `json:"config"`
		} `json:"result"`
	}
	if err := q.request(ctx, http.MethodGet, "/collections/"+q.Collection, nil, &existing); err != nil {
		return fmt.Errorf("inspect existing qdrant collection: %w", err)
	}
	dense, exists := existing.Result.Config.Params.Vectors["dense"]
	// dense 维度必须与 EMBEDDING_DIM 一致：换 embedding 模型后维度不符的向量
	// 会被 Qdrant 拒绝或污染检索结果，这里在写入之前就拦截。
	if !exists || dense.Size != dimension {
		return fmt.Errorf("qdrant collection %s dense dimension is %d, configured EMBEDDING_DIM is %d; reset or rebuild the collection", q.Collection, dense.Size, dimension)
	}
	if !strings.EqualFold(dense.Distance, "cosine") {
		return fmt.Errorf("qdrant collection %s dense distance is %q, expected Cosine; reset or rebuild the collection", q.Collection, dense.Distance)
	}
	if _, exists := existing.Result.Config.Params.SparseVectors["sparse"]; !exists {
		return fmt.Errorf("qdrant collection %s does not define the named sparse vector; reset or rebuild the collection", q.Collection)
	}
	return nil
}

// Upsert 批量写入向量点，wait=true 让服务端落盘确认后再返回，
// 保证随后的查询能立即读到这批数据。
func (q Qdrant) Upsert(ctx context.Context, points []Point) error {
	values := make([]map[string]any, len(points))
	for i, point := range points {
		values[i] = map[string]any{"id": point.ID, "vector": map[string]any{"dense": point.Dense, "sparse": point.Sparse}, "payload": point.Payload}
	}
	return q.put(ctx, "/collections/"+q.Collection+"/points?wait=true", map[string]any{"points": values})
}

// DeleteDocument 按 payload 中的 document_id 过滤删除整篇文档的全部块，
// 供文档/图片删除及索引失败回滚时做幂等清理。
func (q Qdrant) DeleteDocument(ctx context.Context, documentID uint64) error {
	return q.post(ctx, "/collections/"+q.Collection+"/points/delete?wait=true", map[string]any{
		"filter": map[string]any{"must": []any{map[string]any{"key": "document_id", "match": map[string]any{"value": documentID}}}},
	})
}

// AliasTarget returns the collection currently addressed by alias.
func (q Qdrant) AliasTarget(ctx context.Context, alias string) (string, bool, error) {
	var output struct {
		Result struct {
			Aliases []struct {
				AliasName      string `json:"alias_name"`
				CollectionName string `json:"collection_name"`
			} `json:"aliases"`
		} `json:"result"`
	}
	if err := q.request(ctx, http.MethodGet, "/aliases", nil, &output); err != nil {
		return "", false, err
	}
	for _, item := range output.Result.Aliases {
		if item.AliasName == alias {
			return item.CollectionName, true, nil
		}
	}
	return "", false, nil
}

// SwitchAlias atomically redirects alias to q.Collection. The target collection
// must already be fully built and validated by the caller.
func (q Qdrant) SwitchAlias(ctx context.Context, alias string) error {
	alias = strings.TrimSpace(alias)
	if alias == "" || alias == q.Collection {
		return fmt.Errorf("alias must be non-empty and differ from target collection")
	}
	current, exists, err := q.AliasTarget(ctx, alias)
	if err != nil {
		return err
	}
	if exists && current == q.Collection {
		return nil
	}
	actions := make([]any, 0, 2)
	if exists {
		actions = append(actions, map[string]any{"delete_alias": map[string]any{"alias_name": alias}})
	}
	actions = append(actions, map[string]any{"create_alias": map[string]any{
		"collection_name": q.Collection, "alias_name": alias,
	}})
	return q.post(ctx, "/collections/aliases", map[string]any{"actions": actions})
}

// Ready 探测 Qdrant 的就绪状态，供启动阶段的依赖检查使用。
func (q Qdrant) Ready(ctx context.Context) error {
	return q.request(ctx, http.MethodGet, "/readyz", nil, nil)
}

func (q Qdrant) put(ctx context.Context, path string, input any) error {
	return q.request(ctx, http.MethodPut, path, input, nil)
}

func (q Qdrant) post(ctx context.Context, path string, input any) error {
	return q.request(ctx, http.MethodPost, path, input, nil)
}

// request 是统一 HTTP 封装：input 非空时 JSON 序列化并带 Content-Type，
// 非 2xx 一律报错；output 非空时把响应体解码到该结构。
func (q Qdrant) request(ctx context.Context, method, path string, input, output any) error {
	var body []byte
	if input != nil {
		body, _ = json.Marshal(input)
	}
	// 先去掉 BaseURL 尾部斜杠再拼路径，避免出现 "//" 导致路由异常。
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(q.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := q.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	// 按百位判断 2xx，统一覆盖 200/201 等所有成功状态码。
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("qdrant %s returned %d", path, response.StatusCode)
	}
	if output != nil {
		// 响应体限量读取 4 MiB，防御异常返回的超大 JSON 占满内存。
		return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(output)
	}
	return nil
}

// Hybrid 通过 Qdrant Query API 并行预取 dense 语义候选和 hashed sparse 词法候选，
// 再在服务端用 RRF 融合。这样既能召回语义近义表达，也能保留专有名词/代码符号匹配。
func (q Qdrant) Hybrid(ctx context.Context, dense []float32, sparse SparseVector, topK int) ([]Hit, error) {
	// 两个 prefetch 组各自取 top-20 候选，再由服务端按 RRF 融合后截取 limit=topK；
	// 稀疏查询体直接透传 indices/values，由 Qdrant 内建 sparse 引擎计算匹配。
	input := map[string]any{
		"prefetch": []any{
			map[string]any{"query": dense, "using": "dense", "limit": 20},
			map[string]any{"query": map[string]any{"indices": sparse.Indices, "values": sparse.Values}, "using": "sparse", "limit": 20},
		},
		"query": map[string]string{"fusion": "rrf"}, "limit": topK, "with_payload": true,
	}
	body, _ := json.Marshal(input)
	url := strings.TrimRight(q.BaseURL, "/") + "/collections/" + q.Collection + "/points/query"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	client := q.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return nil, fmt.Errorf("qdrant query returned %d", response.StatusCode)
	}
	var result struct {
		Result struct {
			Points []Hit `json:"points"`
		} `json:"result"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Result.Points, nil
}

// Dense 仅用稠密向量检索，作为评估中的 dense-only 基线路径。
func (q Qdrant) Dense(ctx context.Context, vector []float32, topK int) ([]Hit, error) {
	return q.query(ctx, map[string]any{"query": vector, "using": "dense", "limit": topK, "with_payload": true})
}

// Sparse 仅用稀疏向量检索，作为评估中的 sparse-only 基线路径。
func (q Qdrant) Sparse(ctx context.Context, vector SparseVector, topK int) ([]Hit, error) {
	return q.query(ctx, map[string]any{"query": vector, "using": "sparse", "limit": topK, "with_payload": true})
}

// query 把 query 参数原样 POST 到 points/query 并解出命中的点列表，
// 是 Dense/Sparse 两个单路检索共用的实现。
func (q Qdrant) query(ctx context.Context, input map[string]any) ([]Hit, error) {
	var result struct {
		Result struct {
			Points []Hit `json:"points"`
		} `json:"result"`
	}
	if err := q.request(ctx, http.MethodPost, "/collections/"+q.Collection+"/points/query", input, &result); err != nil {
		return nil, err
	}
	return result.Result.Points, nil
}
