package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	port := "8081"
	if len(os.Args) > 1 {
		port = os.Args[1]
	}

	name := "backend-" + port

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[%s] %s %s", name, r.Method, r.URL.Path)
		w.Header().Set("X-Backend", name)
		fmt.Fprintf(w, "Hello from %s! Path: %s\n", name, r.URL.Path)
	})

	log.Printf("[%s] Listening on :%s", name, port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
