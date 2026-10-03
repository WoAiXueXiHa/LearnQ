package rag

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestQdrantActiveFiltersEveryCandidateStreamBeforeRanking(t *testing.T) {
	ids := []string{"active-first", "active-second"}
	var requests []map[string]any
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, body)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"result":{"points":[]}}`)), Header: make(http.Header)}, nil
	})}
	q := Qdrant{BaseURL: "http://qdrant.test", Collection: "test", Client: client}
	if _, err := q.DenseActive(context.Background(), []float32{1}, 1, ids); err != nil {
		t.Fatal(err)
	}
	if _, err := q.SparseActive(context.Background(), Sparse("中文"), 1, ids); err != nil {
		t.Fatal(err)
	}
	if _, err := q.HybridActive(context.Background(), []float32{1}, Sparse("中文"), 1, ids); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"must": []any{map[string]any{"key": "chunk_id", "match": map[string]any{"any": []any{"active-first", "active-second"}}}}}
	if len(requests) != 3 {
		t.Fatalf("requests=%d", len(requests))
	}
	for i, body := range requests {
		if !reflect.DeepEqual(body["filter"], want) {
			t.Fatalf("query%d missing active filter: %#v", i, body)
		}
		if body["limit"] != float64(1) {
			t.Fatalf("query%d lost topK", i)
		}
	}
	prefetch := requests[2]["prefetch"].([]any)
	for _, candidate := range prefetch {
		if !reflect.DeepEqual(candidate.(map[string]any)["filter"], want) {
			t.Fatalf("candidate stream allows retired versions: %#v", candidate)
		}
	}
	for _, empty := range [][]string{nil, {}} {
		q.DenseActive(context.Background(), nil, 1, empty)
		q.SparseActive(context.Background(), SparseVector{}, 1, empty)
		q.HybridActive(context.Background(), nil, SparseVector{}, 1, empty)
	}
	if len(requests) != 3 {
		t.Fatal("empty active scope sent unconstrained query")
	}
}
