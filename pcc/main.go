package main

import (
    "log"
    "net/http"
)

func main(){
    server := &http.Server{
        Addr: ":8080",
    }

    http.HandleFunc("/", handler)

    log.Println("Starting file server on port 8080")
    server.ListenAndServe()
}

func handler(w http.ResponseWriter, r *http.Request) {
    filePath := r.URL.Path

    http.ServeFile(w, r, filePath)
}
