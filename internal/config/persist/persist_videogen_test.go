package persist

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harnessclaw-go/internal/config"
)

func writeTmpVG(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSetVideoGenRoundTrip(t *testing.T) {
	t.Parallel()
	p := writeTmpVG(t, "server:\n  addr: \":8080\"\nagent:\n  primary: openai:gpt\n")
	f, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.VideoGenConfig{
		Providers: map[string]config.VideoProviderConfig{
			"doubao": {
				APIKey:  "sk-secret",
				BaseURL: "https://ark.example/api/v3",
				Endpoints: map[string]config.VideoEndpointConfig{
					"seedance-lite-i2v": {Model: "doubao-seedance-x"},
				},
			},
		},
	}
	if err := f.SetVideoGen(cfg); err != nil {
		t.Fatal(err)
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := config.Load(p)
	if err != nil {
		t.Fatalf("config.Load after SetVideoGen: %v", err)
	}
	got := reloaded.VideoGen.Providers["doubao"]
	if got.APIKey != "sk-secret" || got.Endpoints["seedance-lite-i2v"].Model != "doubao-seedance-x" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), "primary: openai:gpt") {
		t.Fatalf("SetVideoGen clobbered unrelated keys:\n%s", raw)
	}
}
