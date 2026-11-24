package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func main() {
	s := bufio.NewScanner(os.Stdin)
	s.Buffer(make([]byte, 4096), 1024*1024)
	for s.Scan() {
		var req map[string]json.RawMessage
		if json.Unmarshal(s.Bytes(), &req) != nil {
			os.Exit(4)
		}
		var method string
		_ = json.Unmarshal(req["method"], &method)
		switch method {
		case "initialize":
			fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"protocolVersion\":1,\"authMethods\":[],\"agentCapabilities\":{}}}\n", req["id"])
		case "session/new":
			fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"sessionId\":\"blocked-session\"}}\n", req["id"])
			time.Sleep(30 * time.Second)
			return
		default:
			os.Exit(5)
		}
	}
}
