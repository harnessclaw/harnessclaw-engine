//go:build integration

package doubao

import (
	"context"
	"os"
	"testing"
	"time"

	videogen "harnessclaw-go/internal/tools/builtin/videogen"
	"go.uber.org/zap"
)

// Run manually: ARK_API_KEY=xxx ARK_MODEL=doubao-seedance-1-0-lite-i2v-250428 \
//   go test -tags=integration -run TestIntegrationSmoke ./internal/tools/builtin/videogen/providers/doubao/ -v
func TestIntegrationSmoke(t *testing.T) {
	key := os.Getenv("ARK_API_KEY")
	if key == "" {
		t.Skip("ARK_API_KEY not set")
	}
	model := os.Getenv("ARK_MODEL")
	if model == "" {
		model = "doubao-seedance-1-0-lite-i2v-250428"
	}
	p := NewProvider(zap.NewNop())
	ep := videogen.EndpointRef{Provider: "doubao", Model: model, APIKey: key}
	sub, err := p.SubmitTask(context.Background(), videogen.SubmitRequest{
		Endpoint: ep, Prompt: "a calico cat stretching, cozy room", DurationS: 5, AspectRatio: "16:9",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	t.Logf("task_id=%s", sub.TaskID)
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		q, err := p.QueryTask(context.Background(), videogen.QueryRequest{Endpoint: ep, TaskID: sub.TaskID})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if q.Status == videogen.StatusSucceeded {
			data, _, err := p.DownloadVideo(context.Background(), q.VideoURL)
			if err != nil || len(data) == 0 {
				t.Fatalf("download: %v len=%d", err, len(data))
			}
			t.Logf("downloaded %d bytes", len(data))
			return
		}
		if q.Status == videogen.StatusFailed {
			t.Fatalf("task failed: %s %s", q.ErrorCode, q.ErrorMessage)
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatal("task did not complete within 5 minutes")
}
