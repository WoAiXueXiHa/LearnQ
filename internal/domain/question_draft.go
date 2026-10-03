package domain

// QuestionDraft is the explicit authoring view, separate from the learner view.
type ReferenceItem struct {
	Text        string   `json:"text"`
	ChunkIDs    []string `json:"chunk_ids"`
	ImageRefIDs []uint64 `json:"image_ref_ids"`
}

type QuestionDraft struct {
	ReferenceItems  []ReferenceItem `json:"reference_items,omitempty"`
	Prompt          string          `json:"prompt"`
	KnowledgePoints []string        `json:"knowledge_points"`
	ReferencePoints []string        `json:"reference_points,omitempty"`
	ChunkIDs        []string        `json:"chunk_ids"`
	ImageRefIDs     []uint64        `json:"image_ref_ids"`
}
