package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestEmitterPostsTracesAndMetricsOverOTLPHTTP(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("method=%s", request.Method)
		}
		if contentType := request.Header.Get("Content-Type"); !strings.Contains(contentType, "application/x-protobuf") {
			t.Errorf("content type=%q", contentType)
		}
		mu.Lock()
		counts[request.URL.Path]++
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/x-protobuf")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("TEST_OTEL_HTTP_ENDPOINT", strings.TrimPrefix(server.URL, "http://"))

	if err := run(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if counts["/v1/traces"] == 0 || counts["/v1/metrics"] == 0 {
		t.Fatalf("OTLP requests=%v", counts)
	}
}
