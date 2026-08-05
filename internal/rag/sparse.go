package rag

import (
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// SparseVector 是哈希稀疏向量：Indices 升序且不重复（Qdrant 的硬性要求），
// Values 与 Indices 一一对应；两字段按此结构序列化供 Qdrant 写入与查询。
type SparseVector struct {
	Indices []uint32  `json:"indices"`
	Values  []float32 `json:"values"`
}

// Sparse 把文本转成去重后的稀疏向量：英文按连续字母数字词、中文按单字与相邻双字
// 切分，token 经 FNV-1a 哈希得到 index（counts 天然去重），最后按 index 升序输出。
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
			// 相邻双字一并入索引：中文单字歧义大，双字对提升词法召回精度更有效。
			if i+1 < len(chinese) {
				counts[hashToken(string(chinese[i:i+2]))]++
			}
		}
		chinese = chinese[:0]
	}
	// 三类字符各归其路：中文进中文字符缓冲，字母数字拼英文词；
	// 其余符号视为分词边界，同时冲刷两个缓冲，防止中英 token 粘连。
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
	// Qdrant 要求 sparse indices 升序且唯一；counts 键已保证唯一，
	// 此处只需排序，token 种类有限，插入排序即可满足。
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

// hashToken 用 FNV-1a 把 token 定长映射到 32 位空间；哈希碰撞会使不同 token
// 共用一个 index（计数合并），这是零词表方案的既定精度取舍。
func hashToken(token string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(token))
	return h.Sum32()
}

// RRF 按名次倒数加分融合多路检索结果，返回按总分降序的 ID 列表；k 非正时回退 60。
// 名次位置固定偏移 k+1，同一 ID 出现在多路结果中会累计得分。
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
			// 选择排序降序；同分时按 ID 字典序二次比较，保证输出确定可复现。
			if scores[out[j]] > scores[out[i]] || (scores[out[j]] == scores[out[i]] && out[j] < out[i]) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
