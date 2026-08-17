package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/bootstrap"
	"github.com/WoAiXueXiHa/LearnQ/internal/config"
	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/indexer"
	"github.com/WoAiXueXiHa/LearnQ/internal/rag"
	"github.com/WoAiXueXiHa/LearnQ/internal/store"
)

func main() {
	documentID := flag.Uint64("document-id", 0, "enqueue one document for reindex")
	all := flag.Bool("all", false, "process all eligible documents")
	limit := flag.Int("limit", 1000, "maximum documents selected by --all")
	shadowCollection := flag.String("shadow-collection", "", "rebuild ready documents into this new collection")
	alias := flag.String("alias", "", "atomically switch this alias after a successful shadow rebuild")
	flag.Parse()
	if (*documentID == 0) == !*all || *limit <= 0 {
		log.Fatal("choose exactly one of --document-id or --all; --limit must be positive")
	}
	if *shadowCollection != "" && (!*all || *alias == "") {
		log.Fatal("--shadow-collection requires --all and --alias")
	}
	if *shadowCollection == "" && *alias != "" {
		log.Fatal("--alias is only valid with --shadow-collection")
	}

	cfg := config.Load()
	if err := cfg.ValidateAPI(); err != nil {
		log.Fatal(err)
	}
	db, err := bootstrap.MySQL(cfg)
	if err != nil {
		log.Fatal(err)
	}
	s := store.New(db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	if *shadowCollection != "" {
		shadowRebuild(ctx, cfg, s, *shadowCollection, *alias, *limit)
		return
	}
	if *documentID != 0 {
		document, task, err := s.ReindexDocument(ctx, *documentID)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("enqueued document=%d task=%d\n", document.ID, task.ID)
		return
	}

	var documents []domain.Document
	if err := db.WithContext(ctx).Where("status IN ('ready','failed')").
		Order("id").Limit(*limit).Find(&documents).Error; err != nil {
		log.Fatal(err)
	}
	enqueued := 0
	for _, document := range documents {
		_, task, err := s.ReindexDocument(ctx, document.ID)
		if err != nil {
			log.Printf("skip document=%d: %v", document.ID, err)
			continue
		}
		enqueued++
		fmt.Printf("enqueued document=%d task=%d\n", document.ID, task.ID)
	}
	fmt.Printf("summary selected=%d enqueued=%d skipped=%d\n", len(documents), enqueued, len(documents)-enqueued)
}

func shadowRebuild(ctx context.Context, cfg config.Config, s *store.Store, collection, alias string, limit int) {
	if collection == alias {
		log.Fatal("shadow collection must differ from alias")
	}
	var documents []domain.Document
	if err := s.DB.WithContext(ctx).Where("status='ready'").Order("id").Limit(limit).Find(&documents).Error; err != nil {
		log.Fatal(err)
	}
	if len(documents) == 0 {
		log.Fatal("no ready documents selected; alias was not changed")
	}
	_, embedding := bootstrap.Models(cfg)
	vectors := rag.Qdrant{
		BaseURL: cfg.QdrantURL, Collection: collection,
		Client: &http.Client{Timeout: 30 * time.Second},
	}
	version := fmt.Sprintf("collection=%s;embedding=%s;dim=%d;chunk=persisted", collection, cfg.AIEmbeddingModel, cfg.EmbeddingDim)
	builder := &indexer.Indexer{
		Store: s, Embedding: embedding, Vectors: vectors,
		Dimension: cfg.EmbeddingDim, Version: version,
	}
	for index, document := range documents {
		if err := builder.ShadowDocument(ctx, document.ID); err != nil {
			log.Fatalf("shadow rebuild failed document=%d after=%d: %v; alias was not changed", document.ID, index, err)
		}
		fmt.Printf("rebuilt document=%d target=%s\n", document.ID, collection)
	}
	if err := vectors.SwitchAlias(ctx, alias); err != nil {
		log.Fatalf("shadow collection is complete but alias switch failed: %v", err)
	}
	ids := make([]uint64, len(documents))
	for index := range documents {
		ids[index] = documents[index].ID
	}
	if err := s.DB.WithContext(ctx).Model(&domain.Document{}).
		Where("id IN ? AND status='ready'", ids).
		Updates(map[string]any{"index_version": version, "updated_at": time.Now().UTC()}).Error; err != nil {
		log.Fatalf("alias switched but index version metadata update failed: %v", err)
	}
	fmt.Printf("summary rebuilt=%d alias=%s target=%s\n", len(documents), alias, collection)
}
