package protocol_test

import (
	"testing"

	"proxyma/internal/protocol"
)

func TestWithServiceQuery(t *testing.T) {
	got := protocol.WithServiceQuery(protocol.PathRel(protocol.PathServicesSubmit), "ocr")
	if got != "services/submit?service=ocr" {
		t.Errorf("relative path = %q", got)
	}
	got = protocol.WithServiceQuery("https://cb.example/callback?x=1", "ocr")
	if got != "https://cb.example/callback?service=ocr&x=1" && got != "https://cb.example/callback?x=1&service=ocr" {
		t.Errorf("absolute URL with existing query = %q", got)
	}
	got = protocol.WithServiceQuery("https://cb.example/callback", "ocr")
	if got != "https://cb.example/callback?service=ocr" {
		t.Errorf("absolute URL = %q", got)
	}
}
