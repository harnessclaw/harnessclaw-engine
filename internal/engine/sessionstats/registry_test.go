package sessionstats

import "testing"

func TestRegistry_GetOrCreate_StableInstance(t *testing.T) {
	r := NewRegistry()
	a := r.GetOrCreate("sess_1")
	b := r.GetOrCreate("sess_1")
	if a != b {
		t.Errorf("GetOrCreate returned distinct trackers for same session")
	}
}

func TestRegistry_Get_ReturnsNilWhenAbsent(t *testing.T) {
	r := NewRegistry()
	if got := r.Get("nope"); got != nil {
		t.Errorf("Get on absent session = %v, want nil", got)
	}
}

func TestRegistry_Drop_RemovesEntry(t *testing.T) {
	r := NewRegistry()
	r.GetOrCreate("sess_1")
	r.Drop("sess_1")
	if got := r.Get("sess_1"); got != nil {
		t.Errorf("Drop did not remove entry: %v", got)
	}
}

func TestRegistry_ConcurrentGetOrCreate(t *testing.T) {
	r := NewRegistry()
	const n = 50
	done := make(chan *Tracker, n)
	for i := 0; i < n; i++ {
		go func() { done <- r.GetOrCreate("sess_x") }()
	}
	first := <-done
	for i := 1; i < n; i++ {
		if got := <-done; got != first {
			t.Errorf("concurrent GetOrCreate returned distinct tracker")
		}
	}
}
