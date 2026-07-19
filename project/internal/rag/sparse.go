package rag

import (
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

type SparseVector struct {
	Indices []uint32  `json:"indices"`
	Values  []float32 `json:"values"`
}

func Sparse(text string) SparseVector {
	// 这是无需词典的轻量词法召回：英文按连续字母数字分词，中文同时保留单字和双字。
	// token 哈希到固定 uint32 空间，换取零词表部署；哈希碰撞是该方案接受的精度代价。
	counts := map[uint32]int{}
	var english strings.Builder
	var chinese []rune
	flushEnglish := func() {
		if english.Len() > 0 {
			counts[hashToken(strings.ToLower(english.String()))]++
			english.Reset()
		}
	}
	flushChinese := func() {
		for i, r := range chinese {
			counts[hashToken(string(r))]++
			if i+1 < len(chinese) {
				counts[hashToken(string(chinese[i:i+2]))]++
			}
		}
		chinese = chinese[:0]
	}
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			flushEnglish()
			chinese = append(chinese, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushChinese()
			english.WriteRune(r)
		default:
			flushEnglish()
			flushChinese()
		}
	}
	flushEnglish()
	flushChinese()
	indices := make([]uint32, 0, len(counts))
	for index := range counts {
		indices = append(indices, index)
	}
	for i := 1; i < len(indices); i++ {
		for j := i; j > 0 && indices[j] < indices[j-1]; j-- {
			indices[j], indices[j-1] = indices[j-1], indices[j]
		}
	}
	values := make([]float32, len(indices))
	for i, index := range indices {
		// 对词频取对数，保留重复词信号但避免高频词完全支配稀疏得分。
		values[i] = float32(1 + math.Log(float64(counts[index])))
	}
	return SparseVector{Indices: indices, Values: values}
}

func hashToken(token string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(token))
	return h.Sum32()
}

func RRF(rankings [][]string, k int) []string {
	// RRF 只融合名次，不要求 dense 与 sparse 的原始分数处在同一尺度。
	if k <= 0 {
		k = 60
	}
	scores := map[string]float64{}
	for _, ranking := range rankings {
		for rank, id := range ranking {
			scores[id] += 1 / float64(k+rank+1)
		}
	}
	out := make([]string, 0, len(scores))
	for id := range scores {
		out = append(out, id)
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if scores[out[j]] > scores[out[i]] || (scores[out[j]] == scores[out[i]] && out[j] < out[i]) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
