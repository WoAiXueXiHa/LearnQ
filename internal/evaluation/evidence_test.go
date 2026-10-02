package evaluation

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/WoAiXueXiHa/LearnQ/internal/model"
)

func TestEvidencePointResolution(t *testing.T) {
	article := "# 持久化\n父进程 fork 子进程\n写时拷贝保留旧快照\n另一处等价依据\n"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(article)))
	chunks := []EvidenceChunk{{ID: "a", Content: "父进程 fork 子进程\n写时拷贝保留旧快照", StartLine: 2, EndLine: 3}, {ID: "b", Content: "另一处等价依据", StartLine: 4, EndLine: 4}}
	for _, tc := range []struct {
		name   string
		lines  [][]int
		hash   string
		chunks []EvidenceChunk
		bad    bool
	}{
		{"full span", [][]int{{2, 3}}, hash, chunks, false},
		{"alternative", [][]int{{2, 3}, {4, 4}}, hash, chunks, false},
		{"wrong hash", [][]int{{2, 3}}, strings.Repeat("0", 64), chunks, true},
		{"zero line", [][]int{{0, 2}}, hash, chunks, true},
		{"reversed", [][]int{{3, 2}}, hash, chunks, true},
		{"outside article", [][]int{{2, 99}}, hash, chunks, true},
		{"partial text", [][]int{{2, 3}}, hash, []EvidenceChunk{{ID: "a", Content: "父进程 fork 子进程", StartLine: 2, EndLine: 3}}, true},
		{"no chunks", [][]int{{2, 3}}, hash, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cases := []Case{{ID: "p", Question: "q", DocumentID: 7, ArticleSHA256: tc.hash, EvidencePoints: []EvidencePoint{{Point: "机制", Lines: tc.lines}}}}
			resolved, err := ResolveEvidencePoints(context.Background(), cases, func(_ context.Context, id uint64) (string, []EvidenceChunk, error) {
				if id != 7 {
					t.Fatalf("document=%d", id)
				}
				return article, tc.chunks, nil
			})
			if tc.bad {
				if err == nil {
					t.Fatal("accepted invalid evidence")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(resolved[0].EvidencePoints[0].ChunkIDs) == 0 {
				t.Fatal("no mapped evidence")
			}
		})
	}
}

func TestEvidencePointCoverageUsesAlternatives(t *testing.T) {
	cases := []Case{{ID: "p", Question: "q", EvidencePoints: []EvidencePoint{{Point: "任一同义跨度", ChunkIDs: []string{"a", "b"}, Alternatives: [][]string{{"a"}, {"b"}}}, {Point: "缺失机制", ChunkIDs: []string{"z"}, Alternatives: [][]string{{"z"}}}}}}
	report, err := Run(context.Background(), cases, model.Fake{Dimension: 8}, fakeRetriever{}, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range RetrieverNames {
		points := report.Cases[0].EvidenceByRetriever[name]
		if len(points) != 2 || !points[0].Covered || points[1].Covered {
			t.Fatalf("%s evidence=%#v", name, points)
		}
	}
	if !strings.Contains(Markdown(report), "缺失机制") {
		t.Fatal("report hides missing point")
	}
}

func TestEvidenceRequiresAllChunksForLongLine(t *testing.T) {
	first := "开头" + strings.Repeat("甲", 800)
	second := "后半" + strings.Repeat("乙", 50) + "关键结论"
	article := first + second + "\n"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(article)))
	cases := []Case{{ID: "long", Question: "q", DocumentID: 7, ArticleSHA256: hash, EvidencePoints: []EvidencePoint{{Point: "完整论点", Lines: [][]int{{1, 1}}}}}}
	resolved, err := ResolveEvidencePoints(context.Background(), cases, func(context.Context, uint64) (string, []EvidenceChunk, error) {
		return article, []EvidenceChunk{{ID: "first", Content: first, StartLine: 1, EndLine: 1}, {ID: "second", Content: second, StartLine: 1, EndLine: 1}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved[0].EvidencePoints[0].Alternatives) != 1 || len(resolved[0].EvidencePoints[0].Alternatives[0]) != 2 {
		t.Fatalf("mapping=%#v", resolved)
	}
	resolved[0].EvidencePoints[0].Alternatives = [][]string{{"a", "z"}}
	resolved[0].EvidencePoints[0].ChunkIDs = []string{"a", "z"}
	report, err := Run(context.Background(), resolved, model.Fake{Dimension: 8}, fakeRetriever{}, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range RetrieverNames {
		if report.Cases[0].EvidenceByRetriever[name][0].Covered {
			t.Fatalf("%s covers only half the required span", name)
		}
	}
}

func TestEvidenceRejectsAmbiguousRepeatedTextWithoutOffset(t *testing.T) {
	article := "重复重复重复\n"
	cases := []Case{{ID: "repeat", Question: "q", DocumentID: 7, ArticleSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(article))), EvidencePoints: []EvidencePoint{{Point: "重复行", Lines: [][]int{{1, 1}}}}}}
	_, err := ResolveEvidencePoints(context.Background(), cases, func(context.Context, uint64) (string, []EvidenceChunk, error) {
		return article, []EvidenceChunk{{ID: "a", Content: "重复", StartLine: 1, EndLine: 1}}, nil
	})
	if err == nil {
		t.Fatal("ambiguous text accepted without exact position")
	}
}

func TestEvidenceCoverageAcceptsAnotherCompleteChunkCombination(t *testing.T) {
	article := "abcdefghijkl\n"
	zero, six := 0, 6
	cases := []Case{{ID: "union", Question: "q", DocumentID: 7, ArticleSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(article))), EvidencePoints: []EvidencePoint{{Point: "完整跨度", Lines: [][]int{{1, 1}}}}}}
	resolved, err := ResolveEvidencePoints(context.Background(), cases, func(context.Context, uint64) (string, []EvidenceChunk, error) {
		return article, []EvidenceChunk{
			{ID: "unused-left", Content: article[:8], StartLine: 1, EndLine: 1, StartByte: &zero},
			{ID: "unused-right", Content: article[6:12], StartLine: 1, EndLine: 1, StartByte: &six},
			{ID: "a", Content: article[:6], StartLine: 1, EndLine: 1, StartByte: &zero},
			{ID: "b", Content: article[6:12], StartLine: 1, EndLine: 1, StartByte: &six},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), resolved, model.Fake{Dimension: 8}, fakeRetriever{}, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Cases[0].EvidenceByRetriever["sparse-only"][0].Covered {
		t.Fatal("retrieved alternative complete union was marked missing")
	}
	if report.Cases[0].EvidenceByRetriever["dense-only"][0].Covered {
		t.Fatal("incomplete union was marked covered")
	}
}
