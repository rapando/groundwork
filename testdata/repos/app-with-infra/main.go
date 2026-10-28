package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"example.com/app-with-infra/internal/greet"
)

func handler(env string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, greet.Message(env))
	}
}

func main() {
	addr := ":" + getenv("PORT", "8080")
	http.Handle("/", handler(getenv("APP_ENV", "dev")))
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
