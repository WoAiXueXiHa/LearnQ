package rag

import "math"

// RecallAtK 计算前 K 个结果中相关块的占比：相关判定只看 relevant 值是否 >0，
// 与等级无关；无相关标注时返回 0，k 超过结果数时按实际结果数截断。
func RecallAtK(results []string, relevant map[string]int, k int) float64 {
	// 分母只计算 relevance>0 的真实相关块；0 级 judgment 是显式负样本。
	relevantCount := 0
	for _, relevance := range relevant {
		if relevance > 0 {
			relevantCount++
		}
	}
	if relevantCount == 0 || k <= 0 {
		return 0
	}
	if k > len(results) {
		k = len(results)
	}
	found := 0
	seen := make(map[string]struct{}, k)
	for _, id := range results[:k] {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		if relevant[id] > 0 {
			found++
		}
	}
	return float64(found) / float64(relevantCount)
}

// NDCGAtK 按排名位置折扣累加相关性得分，再除以理想排序的对应累加值，
// 归一化到 [0,1]；理想分全为 0 时返回 0 避免除零。
func NDCGAtK(results []string, relevant map[string]int, k int) float64 {
	// NDCG 同时考虑相关性等级和排名位置，并用理想排序归一化到 [0,1]。
	if k > len(results) {
		k = len(results)
	}
	var dcg float64
	// DCG 标准公式：等级越高增益越大（2^rel - 1），位置越靠后折扣越重
	// （log2(i+2)，i 从 0 起，位置 1 对应分母 log2 2）。
	for i, id := range results[:k] {
		rel := relevant[id]
		dcg += (math.Pow(2, float64(rel)) - 1) / math.Log2(float64(i+2))
	}
	// 理想排序与检索结果无关，只需把标注等级降序排列后按同一公式计算；
	// 对等级值排序比按文档排序便宜得多。
	ideal := make([]int, 0, len(relevant))
	for _, rel := range relevant {
		ideal = append(ideal, rel)
	}
	for i := 0; i < len(ideal); i++ {
		for j := i + 1; j < len(ideal); j++ {
			if ideal[j] > ideal[i] {
				ideal[i], ideal[j] = ideal[j], ideal[i]
			}
		}
	}
	if k > len(ideal) {
		k = len(ideal)
	}
	var idcg float64
	for i, rel := range ideal[:k] {
		idcg += (math.Pow(2, float64(rel)) - 1) / math.Log2(float64(i+2))
	}
	// 所有标注等级均为 0 时理想分也是 0，直接返回 0 避免除零。
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

// RetrievalCoverage 计算标注块被检索候选集覆盖的比例；它不评价生成答案的引用质量。
func RetrievalCoverage(required, retrieved []string) float64 {
	// 检索覆盖率关注标注证据是否进入候选集，与生成答案的语言质量解耦。
	// 没有必须引用的标注时视为全覆盖，避免空集被记 0 分。
	if len(required) == 0 {
		return 1
	}
	set := map[string]bool{}
	for _, id := range retrieved {
		set[id] = true
	}
	found := 0
	for _, id := range required {
		if set[id] {
			found++
		}
	}
	return float64(found) / float64(len(required))
}
