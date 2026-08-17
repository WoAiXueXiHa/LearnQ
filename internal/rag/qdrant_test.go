package rag

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestQdrantUsesNamedDenseSparseAndRRF(t *testing.T) {
	var paths []string
	var requests []map[string]any
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		requests = append(requests, body)
		responseBody := `{"result":{"status":"ok"}}`
		if strings.HasSuffix(r.URL.Path, "/points/query") {
			responseBody = `{"result":{"points":[{"id":"00000000-0000-0000-0000-000000000001","score":0.9,"payload":{"chunk_id":"a"}}]}}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(responseBody))}, nil
	})}
	q := Qdrant{BaseURL: "http://qdrant.test", Collection: "learnq_chunks", Client: client}
	if err := q.EnsureCollection(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	point := Point{ID: "00000000-0000-0000-0000-000000000001", Dense: []float32{1, 0, 0}, Sparse: Sparse("Redis 队列"), Payload: map[string]any{"document_id": uint64(1)}}
	if err := q.Upsert(context.Background(), []Point{point}); err != nil {
		t.Fatal(err)
	}
	hits, err := q.Hybrid(context.Background(), []float32{1, 0, 0}, Sparse("Redis"), 5)
	if err != nil || len(hits) != 1 || hits[0].Payload["chunk_id"] != "a" {
		t.Fatalf("hits=%#v err=%v", hits, err)
	}
	if len(paths) != 3 || paths[0] != "PUT /collections/learnq_chunks" || paths[2] != "POST /collections/learnq_chunks/points/query" {
		t.Fatalf("paths=%v", paths)
	}
	collectionVectors, ok := requests[0]["vectors"].(map[string]any)
	if !ok || collectionVectors["dense"] == nil || requests[0]["sparse_vectors"] == nil {
		t.Fatalf("collection request=%#v", requests[0])
	}
	prefetch, ok := requests[2]["prefetch"].([]any)
	if !ok || len(prefetch) != 2 {
		t.Fatalf("query request=%#v", requests[2])
	}
	query, ok := requests[2]["query"].(map[string]any)
	if !ok || query["fusion"] != "rrf" {
		t.Fatalf("query=%#v", requests[2]["query"])
	}
}

func TestEnsureCollectionRejectsDimensionDrift(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPut {
			return &http.Response{StatusCode: http.StatusConflict, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
		body := `{"result":{"config":{"params":{"vectors":{"dense":{"size":1536,"distance":"Cosine"}},"sparse_vectors":{"sparse":{}}}}}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	q := Qdrant{BaseURL: "http://qdrant.test", Collection: "learnq_chunks", Client: client}
	err := q.EnsureCollection(context.Background(), 64)
	if err == nil || !strings.Contains(err.Error(), "configured EMBEDDING_DIM is 64") {
		t.Fatalf("dimension drift error = %v", err)
	}
}

func TestEnsureCollectionAcceptsMatchingExistingDimension(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPut {
			return &http.Response{StatusCode: http.StatusConflict, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
		body := `{"result":{"config":{"params":{"vectors":{"dense":{"size":64,"distance":"Cosine"}},"sparse_vectors":{"sparse":{}}}}}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	q := Qdrant{BaseURL: "http://qdrant.test", Collection: "learnq_chunks", Client: client}
	if err := q.EnsureCollection(context.Background(), 64); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureCollectionRejectsIncompatibleDistanceOrMissingSparseVector(t *testing.T) {
	tests := []struct {
		name, body, expected string
	}{
		{
			name:     "distance",
			body:     `{"result":{"config":{"params":{"vectors":{"dense":{"size":64,"distance":"Dot"}},"sparse_vectors":{"sparse":{}}}}}}`,
			expected: "expected Cosine",
		},
		{
			name:     "sparse",
			body:     `{"result":{"config":{"params":{"vectors":{"dense":{"size":64,"distance":"Cosine"}},"sparse_vectors":{}}}}}`,
			expected: "named sparse vector",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodPut {
					return &http.Response{StatusCode: http.StatusConflict, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})}
			q := Qdrant{BaseURL: "http://qdrant.test", Collection: "learnq_chunks", Client: client}
			err := q.EnsureCollection(context.Background(), 64)
			if err == nil || !strings.Contains(err.Error(), test.expected) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestSwitchAliasUsesOneAtomicUpdate(t *testing.T) {
	var update map[string]any
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"result":{"aliases":[{"alias_name":"learnq_live","collection_name":"learnq_old"}]}}`
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
				t.Fatal(err)
			}
			body = `{"result":true}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	q := Qdrant{BaseURL: "http://qdrant.test", Collection: "learnq_new", Client: client}
	if err := q.SwitchAlias(context.Background(), "learnq_live"); err != nil {
		t.Fatal(err)
	}
	actions, ok := update["actions"].([]any)
	if !ok || len(actions) != 2 {
		t.Fatalf("alias update=%#v", update)
	}
	deleted := actions[0].(map[string]any)["delete_alias"].(map[string]any)
	created := actions[1].(map[string]any)["create_alias"].(map[string]any)
	if deleted["alias_name"] != "learnq_live" ||
		created["alias_name"] != "learnq_live" || created["collection_name"] != "learnq_new" {
		t.Fatalf("actions=%#v", actions)
	}
}
