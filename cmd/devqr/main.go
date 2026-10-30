// Command devqr is a development helper that follows a session's QR stream
// (GET /v1/sessions/{id}/qr/stream) and draws each QR code in the terminal,
// until the web panel does that job.
//
//	ATM_TOKEN=<service token> go run ./cmd/devqr -session <session id>
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"

	"github.com/mdp/qrterminal/v3"
)

func main() {
	baseURL := flag.String("url", "http://localhost:8080", "API base URL")
	sessionID := flag.String("session", "", "session id")
	flag.Parse()

	token := os.Getenv("ATM_TOKEN")
	if *sessionID == "" || token == "" {
		log.Fatal("usage: ATM_TOKEN=... devqr -session <id>")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, *baseURL+"/v1/sessions/"+*sessionID+"/qr/stream", nil)
	if err != nil {
		log.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		log.Fatalf("unexpected status %s", resp.Status)
	}

	var event string
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			switch event {
			case "qr":
				fmt.Println("\nScan: WhatsApp > Linked devices > Link a device")
				qrterminal.GenerateHalfBlock(data, qrterminal.L, os.Stdout)
			case "connected", "closed":
				fmt.Printf("\n%s: %s\n", event, data)
				return
			}
		}
	}
}
