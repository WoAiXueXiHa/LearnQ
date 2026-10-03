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
	shadowCollection := flag.String("shadow-collection", "", "legacy embedding-only shadow build; requires maintenance window with API/worker writes stopped")
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
	var readyCount int64
	if err := s.DB.WithContext(ctx).Model(&domain.Document{}).Where("status='ready'").Count(&readyCount).Error; err != nil {
		log.Fatal(err)
	}
	if readyCount > int64(limit) {
		log.Fatal("shadow rebuild limit excludes ready documents; alias was not changed")
	}
	var documents []domain.Document
	if err := s.DB.WithContext(ctx).Where("status='ready'").Order("id").Limit(limit).Find(&documents).Error; err != nil {
		log.Fatal(err)
	}
	if len(documents) == 0 {
		log.Fatal("no ready documents selected; alias was not changed")
	}
	_, embedding := bootstrap.MeteredModels(cfg, s.DB)
	vectors := rag.Qdrant{
		BaseURL: cfg.QdrantURL, Collection: collection,
		Client: &http.Client{Timeout: 30 * time.Second},
	}
	version := fmt.Sprintf("collection=%s;embedding=%s;dim=%d;chunk=persisted", collection, bootstrap.EmbeddingModelName(cfg), cfg.EmbeddingDim)
	builder := &indexer.Indexer{
		Store: s, Embedding: embedding, Vectors: vectors,
		Dimension: cfg.EmbeddingDim, Version: version,
	}
	for _, document := range documents {
		if document.ActiveIndexID > 0 {
			log.Fatal("shadow alias migration of versioned documents requires a separate embedding-version contract; use ordinary --document-id/--all reindex")
		}
	}
	for index, document := range documents {
		if err := builder.ShadowDocument(ctx, document.ID); err != nil {
			log.Fatalf("shadow rebuild failed document=%d after=%d: %v; alias was not changed", document.ID, index, err)
		}
		fmt.Printf("rebuilt document=%d target=%s\n", document.ID, collection)
	}
	var current []domain.Document
	if err := s.DB.WithContext(ctx).Where("status='ready'").Order("id").Find(&current).Error; err != nil {
		log.Fatal(err)
	}
	if len(current) != len(documents) {
		log.Fatal("ready document set changed during shadow build; alias was not changed")
	}
	for index, document := range documents {
		if current[index].ID != document.ID || current[index].IndexingTaskID != document.IndexingTaskID || current[index].ActiveIndexID != document.ActiveIndexID {
			log.Fatal("document index changed during shadow build; alias was not changed")
		}
	}
	if err := vectors.SwitchAlias(ctx, alias); err != nil {
		log.Fatalf("shadow collection is complete but alias switch failed: %v", err)
	}
	// Shadow re-embedding does not change the source/chunker version contract.
	// The active MySQL version remains immutable; collection provenance is reported here.
	fmt.Printf("summary rebuilt=%d alias=%s target=%s\n", len(documents), alias, collection)
}
