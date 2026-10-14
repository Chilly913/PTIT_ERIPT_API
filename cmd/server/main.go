package main

import (
	"log"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/server"
)

func main() {
	cfg := config.Load()
	if err := server.Run(cfg); err != nil {
		log.Fatal(err)
	}
}
