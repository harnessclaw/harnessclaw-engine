// Command metrics_e2e runs a single in-process Chat round through the
// StatsProvider, persists via the Manager, and asserts that
// GET /api/v1/sessions/{id}/metrics returns a populated SessionStats.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/api/sessionmetrics"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/mock"
	providerstats "harnessclaw-go/internal/provider/stats"
	sessionsqlite "harnessclaw-go/internal/storage/sqlite"
	"harnessclaw-go/pkg/types"
)

func main() {
	logger, _ := zap.NewDevelopment()
	defer logger.Sync()

	dir, err := os.MkdirTemp("", "metrics_e2e_*")
	must(err, "mkdtemp")
	defer os.RemoveAll(dir)

	store, err := sessionsqlite.New(filepath.Join(dir, "sess.db"))
	must(err, "open sqlite")
	defer store.Close()

	reg := sessionstats.NewRegistry()
	mgr := session.NewManager(store, logger, time.Hour)
	mgr.BindStatsRegistry(reg)
	defer mgr.Shutdown()

	// Scripted mock with a fixed usage response.
	inner := mock.New(mock.Response{
		Text:       "ok",
		StopReason: "end_turn",
		Usage: &types.Usage{
			InputTokens:    100,
			OutputTokens:   50,
			CacheRead:      20,
			CacheWrite:     5,
			ThinkingTokens: 8,
		},
	})
	prov := providerstats.New(inner, reg)

	// Open a session through the Manager so the Tracker is created and
	// wired to the persist worker.
	ctx := context.Background()
	sess, err := mgr.GetOrCreate(ctx, "sess_e2e", "ws", "user_1")
	must(err, "GetOrCreate")

	// One real Chat round, with ctx keys attached so the decorator can
	// attribute usage.
	callCtx := sessionstats.WithSessionID(ctx, sess.ID)
	callCtx = sessionstats.WithAgentRunID(callCtx, "run_main")

	stream, err := prov.Chat(callCtx, &provider.ChatRequest{
		Model:     "opus",
		MaxTokens: 1024,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hello"}}},
		},
		System: "you are useful",
	})
	must(err, "prov.Chat")
	for range stream.Events {
		// Drain so the wrapStream callback fires (records usage).
	}
	if err := stream.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "stream.Err: %v\n", err)
		os.Exit(1)
	}

	// Flush the persist worker so the SQLite copy is fresh; HTTP path
	// would normally prefer the live tracker, but this exercises both.
	mgr.FlushStats(ctx, sess.ID)

	// Mount handler and assert.
	handler := sessionmetrics.New(reg, store, logger)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/sessions/" + sess.ID + "/metrics")
	must(err, "GET")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "unexpected status %d: %s\n", resp.StatusCode, string(body))
		os.Exit(1)
	}

	var got types.SessionStats
	must(json.NewDecoder(resp.Body).Decode(&got), "decode")

	// Assertions.
	if got.LLMCalls != 1 {
		fail("LLMCalls = %d, want 1", got.LLMCalls)
	}
	if got.InputTokens != 100 {
		fail("InputTokens = %d, want 100", got.InputTokens)
	}
	if got.OutputTokens != 50 {
		fail("OutputTokens = %d, want 50", got.OutputTokens)
	}
	if got.CacheReadTokens != 20 {
		fail("CacheReadTokens = %d, want 20", got.CacheReadTokens)
	}
	if got.ThinkingTokens != 8 {
		fail("ThinkingTokens = %d, want 8", got.ThinkingTokens)
	}
	if got.ToolCalls != 0 {
		fail("ToolCalls = %d, want 0 (no tools used)", got.ToolCalls)
	}
	if len(got.PerModel) != 1 || got.PerModel[0].Model != "opus" {
		fail("PerModel = %+v, want exactly one row for 'opus'", got.PerModel)
	}
	if got.ContextWindow.Limit != 1024 {
		fail("ContextWindow.Limit = %d, want 1024", got.ContextWindow.Limit)
	}

	fmt.Printf("OK: LLMCalls=%d InputTokens=%d OutputTokens=%d CacheRead=%d Thinking=%d ContextWindow.Used=%d\n",
		got.LLMCalls, got.InputTokens, got.OutputTokens, got.CacheReadTokens, got.ThinkingTokens, got.ContextWindow.Used)
}

func must(err error, what string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", what, err)
		os.Exit(1)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ASSERTION FAILED: "+format+"\n", args...)
	os.Exit(1)
}
