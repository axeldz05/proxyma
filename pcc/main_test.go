package main

import (
	"testing"
	"net/http"
)

func TestHandler(t *testing.T) {
    client := &http.Client{}

    req, err := http.NewRequest("GET", "http://localhost:8080/test.txt", nil)
    if err != nil {
        t.Fatal(err)
    }

    // Send the request
    resp, err := client.Do(req)
    if err != nil {
        t.Fatal(err)
    }

    if resp.StatusCode != http.StatusOK {
        t.Errorf("expected status code %d, got %d", http.StatusOK, resp.StatusCode)
    }

    // Close the response
    resp.Body.Close()
}
