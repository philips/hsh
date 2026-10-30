package cli

import "testing"

func TestMatchPodsPrefix(t *testing.T) {
	pods := []podInfo{
		{ID: "aaa111", Name: "hsh-53bd3fc7e5ba"},
		{ID: "bbb222", Name: "hsh-53cc00000000"},
		{ID: "ccc333", Name: "hsh-9f0000000000"},
	}
	for _, c := range []struct {
		arg  string
		want int
		err  bool
	}{
		{"53bd", 1, false},
		{"hsh-53bd", 1, false},
		{"9f", 1, false},
		{"53", 0, true},
		{"hsh-53bd3fc7e5ba", 1, false},
		{"nomatch", 0, false},
		{"", 0, false},
	} {
		got, err := matchPods(c.arg, pods)
		if (err != nil) != c.err || len(got) != c.want {
			t.Errorf("matchPods(%q) = %d pods, err %v; want %d, err %v", c.arg, len(got), err, c.want, c.err)
		}
	}
}

func TestResolveTargetsCombines(t *testing.T) {
	pods := []podInfo{
		{ID: "aaa111", Name: "hsh-53bd3fc7e5ba", Created: "2024-01-01T00:00:00Z"},
		{ID: "bbb222", Name: "hsh-9f0000000000", Created: "2024-01-02T00:00:00Z"},
	}
	got, err := resolveTargets([]string{"53bd"}, pods, false, true, false, false)
	if err != nil || len(got) != 2 {
		t.Fatalf("latest+arg: %v %v", got, err)
	}
	got, err = resolveTargets([]string{"9f"}, pods, false, true, false, false)
	if err != nil || len(got) != 1 {
		t.Fatalf("dedupe: %v %v", got, err)
	}
}
