package indexer

import "testing"

func TestStableIDIncludesChunkPosition(t *testing.T) {
	first := stableID(7, 0, "same-content")
	second := stableID(7, 1, "same-content")
	if first == second {
		t.Fatal("identical chunk text at different positions must not collide")
	}
	if first != stableID(7, 0, "same-content") {
		t.Fatal("chunk id must remain deterministic")
	}
}
