// ws_smoke is a one-shot WebSocket smoke client used to exercise the
// full L1→L2→L3 chain without the Electron UI. It dials the channel,
// opens a session, posts a single user message, streams frames until
// the turn closes (or a 90s budget elapses), and prints a compact
// per-frame-type tally so the operator can eyeball: did scheduler
// actually run an LLM loop? Did L3 nest under L2? Did emma loop?
//
// Usage:
//
//	go run ./cmd/ws_smoke "你的任务"
//
// Default task is a tiny stand-in that forces emma → scheduler →
// freelancer without burning much budget.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"nhooyr.io/websocket"
)

const (
	wsURL          = "ws://127.0.0.1:8081/v1/ws"
	defaultPrompt  = "请在 /tmp 下创建一个文件 hello_smoke.txt，内容写「smoke ok」。完成后告诉我文件路径。"
	overallBudget  = 90 * time.Second
	idleBudget     = 30 * time.Second
)

func main() {
	prompt := defaultPrompt
	if len(os.Args) > 1 {
		prompt = strings.Join(os.Args[1:], " ")
	}

	ctx, cancel := context.WithTimeout(context.Background(), overallBudget+15*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	})
	if err != nil {
		die("dial %s: %v", wsURL, err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "smoke done")

	sessID := fmt.Sprintf("smoke-%d", time.Now().Unix())
	send(ctx, conn, map[string]any{
		"type":       "session.create",
		"session_id": sessID,
		"user_id":    "ws_smoke",
	})
	send(ctx, conn, map[string]any{
		"type": "user.message",
		"text": prompt,
	})

	fmt.Printf("→ posted task to session %s: %s\n", sessID, truncate(prompt, 60))
	fmt.Println("→ streaming frames…\n")

	frameCounts := map[string]int{}
	cardKindCounts := map[string]int{}
	subagentTypes := map[string]int{}
	var (
		schedulerCompleted   int
		l2L3HierarchyOK      int // L3 freelancer whose parent_card_id is a sub_xxxx (L2), not a call_xxxx (emma tool call)
		l2L3HierarchyBad     int
		emmaSchedulerDispatches int
		lastActivity         = time.Now()
		gotTurnClose         bool
	)
	deadline := time.Now().Add(overallBudget)

	for {
		if time.Now().After(deadline) {
			fmt.Println("\n× overall budget elapsed")
			break
		}
		if time.Since(lastActivity) > idleBudget {
			fmt.Println("\n× idle budget elapsed (server fell silent)")
			break
		}
		readCtx, readCancel := context.WithTimeout(ctx, 5*time.Second)
		_, data, err := conn.Read(readCtx)
		readCancel()
		if err != nil {
			if ctx.Err() != nil || strings.Contains(err.Error(), "deadline") {
				continue
			}
			fmt.Printf("\n× ws read err: %v\n", err)
			break
		}
		lastActivity = time.Now()

		var head struct {
			Type     string `json:"type"`
			Envelope struct {
				CardKind     string `json:"card_kind"`
				CardID       string `json:"card_id"`
				ParentCardID string `json:"parent_card_id"`
				AgentID      string `json:"agent_id"`
				AgentRole    string `json:"agent_role"`
			} `json:"envelope"`
			Payload struct {
				SubagentType string `json:"subagent_type"`
				Status       string `json:"status"`
			} `json:"payload"`
		}
		_ = json.Unmarshal(data, &head)
		frameCounts[head.Type]++
		if head.Envelope.CardKind != "" {
			cardKindCounts[head.Envelope.CardKind]++
		}
		if head.Payload.SubagentType != "" {
			subagentTypes[head.Payload.SubagentType]++
		}

		switch head.Type {
		case "card.add":
			if head.Envelope.CardKind == "agent" && head.Payload.SubagentType == "freelancer" {
				// L3 freelancer card. parent_card_id should be the L2
				// scheduler agent card (format: sess_xxx_sub_xxx), not
				// emma's scheduler tool call (call_xxx).
				if strings.Contains(head.Envelope.ParentCardID, "_sub_") {
					l2L3HierarchyOK++
				} else if strings.HasPrefix(head.Envelope.ParentCardID, "call_") {
					l2L3HierarchyBad++
				}
			}
			if head.Envelope.CardKind == "tool" && head.Envelope.AgentID == "main" {
				// crude check: emma tool calls — could be scheduler.
			}
		case "card.close":
			if head.Envelope.CardKind == "turn" {
				gotTurnClose = true
				fmt.Printf("✓ got turn.close, stopping\n")
				goto done
			}
		case "session.opened":
			fmt.Println("✓ session opened")
		}
		_ = schedulerCompleted // populated below from server log if needed
	}
done:

	// Compact tally to stdout. The hard counters here are the eyeball
	// signals against the new architecture; cross-reference service.log
	// for tool-level detail.
	fmt.Println("\n=== frame tally ===")
	for k, v := range frameCounts {
		fmt.Printf("  %-22s %d\n", k, v)
	}
	fmt.Println("\n=== card kinds ===")
	for k, v := range cardKindCounts {
		fmt.Printf("  %-22s %d\n", k, v)
	}
	fmt.Println("\n=== subagent types ===")
	for k, v := range subagentTypes {
		fmt.Printf("  %-22s %d\n", k, v)
	}
	fmt.Println("\n=== L1/L2/L3 hierarchy check ===")
	fmt.Printf("  L3 freelancer cards parented under L2 sub-card:  %d\n", l2L3HierarchyOK)
	fmt.Printf("  L3 freelancer cards parented under emma toolcall: %d (bad — old bug)\n", l2L3HierarchyBad)
	fmt.Printf("  got turn.close:                                   %v\n", gotTurnClose)
	_ = emmaSchedulerDispatches
}

func send(ctx context.Context, c *websocket.Conn, v map[string]any) {
	b, _ := json.Marshal(v)
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		die("write: %v", err)
	}
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "× "+format+"\n", args...)
	os.Exit(1)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
