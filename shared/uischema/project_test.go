package uischema

import (
	"encoding/json"
	"testing"

	"proxyma/internal/protocol"
)

func TestNormalizePayloadJSON(t *testing.T) {
	t.Parallel()
	if got := NormalizePayloadJSON(""); got != "{}" {
		t.Fatalf("empty=%q", got)
	}
	if got := NormalizePayloadJSON(`{"key":"val"}`); got != `{"key":"val"}` {
		t.Fatalf("passthrough=%q", got)
	}
	if got := NormalizePayloadJSON(`[1,2]`); got != `[1,2]` {
		t.Fatalf("array=%q", got)
	}
	kv := NormalizePayloadJSON("input_path=/tmp/doc.pdf,active=true,count=5,score=3.14")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(kv), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["input_path"] != "/tmp/doc.pdf" || parsed["active"] != true {
		t.Fatalf("parsed=%#v", parsed)
	}
	if parsed["count"] != float64(5) || parsed["score"] != 3.14 {
		t.Fatalf("numbers=%#v", parsed)
	}
}

func TestFormatBytesAndProjectRows(t *testing.T) {
	if FormatBytes(0) != "0 B" {
		t.Fatalf("FormatBytes(0)=%q", FormatBytes(0))
	}
	if FormatBytes(2048) != "2.00 KB" {
		t.Fatalf("FormatBytes(2048)=%q", FormatBytes(2048))
	}
	rows := ProjectRows(
		[]TableColumn{
			{Header: "STATUS", FieldSelector: "online", Format: "status"},
			{Header: "SIZE", FieldSelector: "size", Format: "bytes"},
		},
		[]map[string]any{
			{"online": true, "size": float64(1024)},
			{"online": false, "size": float64(0)},
		},
	)
	if len(rows) != 2 || rows[0][0] != "ONLINE" || rows[0][1] != "1.00 KB" {
		t.Fatalf("unexpected projection: %#v", rows)
	}
	if rows[1][0] != "OFFLINE" {
		t.Fatalf("expected OFFLINE got %q", rows[1][0])
	}
}

func TestBandwidthStatsRows(t *testing.T) {
	rows := BandwidthStatsRows(protocol.BandwidthStats{
		DownloadSpeed: 1024,
		UploadSpeed:   2048,
		TotalReceived: 4096,
		TotalSent:     8192,
	})
	if len(rows) != 4 {
		t.Fatalf("len=%d", len(rows))
	}
	if rows[0]["metric"] != "Download Speed" {
		t.Fatalf("metric=%v", rows[0]["metric"])
	}
}
