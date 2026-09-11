package skill

import (
	"path/filepath"
	"testing"
)

func TestReader_DirectoryPriority(t *testing.T) {
	for _, tc := range []struct {
		name         string
		folderName   bool
		invalidHigh  bool
		highDisabled bool
		lowDisabled  bool
	}{
		{name: "explicit-frontmatter-name"},
		{name: "default-folder-name", folderName: true},
		{name: "invalid-high-copy", invalidHigh: true},
		{name: "manual-high-copy", highDisabled: true},
		{name: "manual-low-copy", lowDisabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			highDir, lowDir := filepath.Join(root, "z-preferred"), filepath.Join(root, "a-fallback")
			highFolder, lowFolder := "z-override", "a-fallback"
			nameField := "name: shared\n"
			if tc.folderName {
				highFolder, lowFolder, nameField = "shared", "shared", ""
			}
			highFM := nameField + "description: HighOnly\nversion: 1.0.0"
			lowFM := nameField + "description: LowOnly\nversion: 2.0.0"
			if tc.highDisabled {
				highFM += "\ndisable-model-invocation: true"
			}
			if tc.lowDisabled {
				lowFM += "\ndisable-model-invocation: true"
			}
			if tc.invalidHigh {
				highFM = "name: shared\ndescription: [unterminated"
			}
			highPath := writeSkill(t, highDir, highFolder, highFM, "High-priority instructions.")
			lowPath := writeSkill(t, lowDir, lowFolder, lowFM, "Low-priority instructions.")
			r := NewReader([]string{highDir, lowDir}, nil)

			want := SkillCard{
				Name: "shared", Description: "HighOnly", Version: "1.0.0",
				Path: filepath.Dir(highPath), DisableModelInvocation: tc.highDisabled,
			}
			wantBody := "High-priority instructions."
			if tc.invalidHigh {
				want.Description, want.Version, want.Path = "LowOnly", "2.0.0", filepath.Dir(lowPath)
				want.DisableModelInvocation = tc.lowDisabled
				wantBody = "Low-priority instructions."
			}
			checkCard := func(got SkillCard) {
				t.Helper()
				if got.Name != want.Name || got.Description != want.Description ||
					got.Version != want.Version || got.Path != want.Path ||
					got.DisableModelInvocation != want.DisableModelInvocation {
					t.Errorf("card = %+v, want selected source %+v", got, want)
				}
			}
			for _, query := range []string{"", "shared", "LowOnly"} {
				got, err := r.Search(query, 20)
				if err != nil {
					t.Fatalf("Search(%q): %v", query, err)
				}
				wantCount := 1
				if want.DisableModelInvocation || (query == "LowOnly" && !tc.invalidHigh) {
					wantCount = 0
				}
				if len(got) != wantCount {
					t.Errorf("Search(%q) returned %d cards, want %d: %+v", query, len(got), wantCount, got)
				} else if wantCount == 1 {
					checkCard(got[0])
				}
			}
			full, err := r.Load("shared")
			if err != nil {
				t.Fatalf("explicit Load: %v", err)
			}
			checkCard(full.SkillCard)
			if full.Body != wantBody {
				t.Errorf("explicit Load body = %q, want %q", full.Body, wantBody)
			}
			model, err := r.LoadForModel("shared")
			if want.DisableModelInvocation {
				if err == nil || model != nil {
					t.Fatalf("manual high-priority copy must block model loading: full=%+v, err=%v", model, err)
				}
			} else {
				if err != nil || model == nil {
					t.Fatalf("LoadForModel: full=%+v, err=%v", model, err)
				}
				checkCard(model.SkillCard)
				if model.Body != wantBody {
					t.Errorf("LoadForModel body = %q, want %q", model.Body, wantBody)
				}
			}
		})
	}
}

func TestReader_DuplicateNamesDoNotConsumeLimit(t *testing.T) {
	root := t.TempDir()
	highDir, lowDir := filepath.Join(root, "z-preferred"), filepath.Join(root, "a-fallback")
	writeSkill(t, highDir, "primary", "name: a-shared\ndescription: common primary", "Primary body.")
	writeSkill(t, lowDir, "fallback", "name: a-shared\ndescription: common fallback", "Fallback body.")
	writeSkill(t, lowDir, "distinct", "name: z-distinct\ndescription: common distinct", "Distinct body.")
	r := NewReader([]string{highDir, lowDir}, nil)
	for _, query := range []string{"", "common"} {
		got, err := r.Search(query, 2)
		if err != nil {
			t.Fatalf("Search(%q): %v", query, err)
		}
		if len(got) != 2 || got[0].Name != "a-shared" || got[1].Name != "z-distinct" {
			t.Errorf("Search(%q, 2) = %+v, want distinct names a-shared,z-distinct", query, got)
		} else if got[0].Description != "common primary" {
			t.Errorf("Search(%q) selected lower-priority metadata: %+v", query, got[0])
		}
	}
}
