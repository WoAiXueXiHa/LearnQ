package main

import (
	"log"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/bootstrap"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/config"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/migrate"
)

func main() {
	db, err := bootstrap.MySQL(config.Load())
	if err != nil {
		log.Fatal(err)
	}
	if err := migrate.Run(db); err != nil {
		log.Fatal(err)
	}
}
