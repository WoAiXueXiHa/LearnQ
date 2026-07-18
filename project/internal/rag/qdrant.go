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

type Qdrant struct {
	BaseURL, Collection string
	Client              *http.Client
}

type Hit struct {
	ID      string         `json:"id"`
	Score   float64        `json:"score"`
	Payload map[string]any `json:"payload"`
}

type Point struct {
	ID      string         `json:"id"`
	Dense   []float32      `json:"-"`
	Sparse  SparseVector   `json:"-"`
	Payload map[string]any `json:"payload"`
}

func (q Qdrant) EnsureCollection(ctx context.Context, dimension int) error {
	body := map[string]any{"vectors": map[string]any{"dense": map[string]any{"size": dimension, "distance": "Cosine"}}, "sparse_vectors": map[string]any{"sparse": map[string]any{}}}
	err := q.put(ctx, "/collections/"+q.Collection, body)
	if err == nil {
		return nil
	}
	if !strings.Contains(err.Error(), "409") {
		return err
	}
	var existing struct {
		Result struct {
			Config struct {
				Params struct {
					Vectors map[string]struct {
						Size int `json:"size"`
					} `json:"vectors"`
				} `json:"params"`
			} `json:"config"`
		} `json:"result"`
	}
	if err := q.request(ctx, http.MethodGet, "/collections/"+q.Collection, nil, &existing); err != nil {
		return fmt.Errorf("inspect existing qdrant collection: %w", err)
	}
	dense, exists := existing.Result.Config.Params.Vectors["dense"]
	if !exists || dense.Size != dimension {
		return fmt.Errorf("qdrant collection %s dense dimension is %d, configured EMBEDDING_DIM is %d; reset or rebuild the collection", q.Collection, dense.Size, dimension)
	}
	return nil
}

func (q Qdrant) Upsert(ctx context.Context, points []Point) error {
	values := make([]map[string]any, len(points))
	for i, point := range points {
		values[i] = map[string]any{"id": point.ID, "vector": map[string]any{"dense": point.Dense, "sparse": point.Sparse}, "payload": point.Payload}
	}
	return q.put(ctx, "/collections/"+q.Collection+"/points?wait=true", map[string]any{"points": values})
}

func (q Qdrant) DeleteDocument(ctx context.Context, documentID uint64) error {
	return q.post(ctx, "/collections/"+q.Collection+"/points/delete?wait=true", map[string]any{
		"filter": map[string]any{"must": []any{map[string]any{"key": "document_id", "match": map[string]any{"value": documentID}}}},
	})
}

func (q Qdrant) Ready(ctx context.Context) error {
	return q.request(ctx, http.MethodGet, "/readyz", nil, nil)
}

func (q Qdrant) put(ctx context.Context, path string, input any) error {
	return q.request(ctx, http.MethodPut, path, input, nil)
}

func (q Qdrant) post(ctx context.Context, path string, input any) error {
	return q.request(ctx, http.MethodPost, path, input, nil)
}

func (q Qdrant) request(ctx context.Context, method, path string, input, output any) error {
	var body []byte
	if input != nil {
		body, _ = json.Marshal(input)
	}
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
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("qdrant %s returned %d", path, response.StatusCode)
	}
	if output != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(output)
	}
	return nil
}

// Hybrid uses Qdrant's Query API: two prefetch branches retrieve dense and
// hashed lexical sparse candidates, then the server performs RRF fusion.
func (q Qdrant) Hybrid(ctx context.Context, dense []float32, sparse SparseVector, topK int) ([]Hit, error) {
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

func (q Qdrant) Dense(ctx context.Context, vector []float32, topK int) ([]Hit, error) {
	return q.query(ctx, map[string]any{"query": vector, "using": "dense", "limit": topK, "with_payload": true})
}

func (q Qdrant) Sparse(ctx context.Context, vector SparseVector, topK int) ([]Hit, error) {
	return q.query(ctx, map[string]any{"query": vector, "using": "sparse", "limit": topK, "with_payload": true})
}

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
