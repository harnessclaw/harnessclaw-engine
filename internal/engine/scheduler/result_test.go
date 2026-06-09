package scheduler

import (
	"testing"

	pkgtypes "harnessclaw-go/pkg/types"
)

func TestOutcome_TypeSwitch(t *testing.T) {
	cases := []struct {
		name string
		o    Outcome
		want string
	}{
		{
			"sync",
			SyncOutcome{
				Content: []pkgtypes.ContentBlock{
					{
						Type: pkgtypes.ContentTypeText,
						Text: "hi",
					},
				},
			},
			"sync",
		},
		{
			"async",
			AsyncOutcome{
				OutputFile: "/tmp/x.jsonl",
				Tailable:   true,
			},
			"async",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			switch tc.o.(type) {
			case SyncOutcome:
				got = "sync"
			case AsyncOutcome:
				got = "async"
			}
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}
