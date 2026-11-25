package main

import (
	"log"

	"koma/internal/app"
)

func main() {
	application := app.New()
	if err := application.Run(); err != nil {
		log.Fatal(err)
	}
}
