package rag

import "math"

func RecallAtK(results []string, relevant map[string]int, k int) float64 {
	if len(relevant) == 0 {
		return 0
	}
	if k > len(results) {
		k = len(results)
	}
	found := 0
	for _, id := range results[:k] {
		if relevant[id] > 0 {
			found++
		}
	}
	return float64(found) / float64(len(relevant))
}

func NDCGAtK(results []string, relevant map[string]int, k int) float64 {
	if k > len(results) {
		k = len(results)
	}
	var dcg float64
	for i, id := range results[:k] {
		rel := relevant[id]
		dcg += (math.Pow(2, float64(rel)) - 1) / math.Log2(float64(i+2))
	}
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
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

func CitationCoverage(required, cited []string) float64 {
	if len(required) == 0 {
		return 1
	}
	set := map[string]bool{}
	for _, id := range cited {
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
