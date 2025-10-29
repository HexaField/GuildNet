package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"
)

type payload struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	NodeCount int32  `json:"nodeCount"`
	CPUMilli  int32  `json:"cpuMilli"`
	MemoryMB  int32  `json:"memoryMb"`
}

func main() {
	host := os.Getenv("GN_HOSTAPP_URL")
	if host == "" {
		log.Println("GN_HOSTAPP_URL not set, exiting")
		return
	}

	id := os.Getenv("GN_SITE_ID")
	if id == "" {
		id = "site-local"
	}

	for {
		p := payload{ID: id, Name: "local-site", NodeCount: 1, CPUMilli: 2000, MemoryMB: 4096}
		b, _ := json.Marshal(p)
		resp, err := http.Post(host+"/api/site/heartbeat", "application/json", bytes.NewReader(b))
		if err != nil {
			log.Printf("heartbeat failed: %v", err)
		} else {
			resp.Body.Close()
		}
		time.Sleep(30 * time.Second)
	}
}
