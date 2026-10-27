package version

import "testing"

func TestString(t *testing.T) {
	cases := []struct {
		in   Info
		want string
	}{
		{Info{Tag: "v1.2.3", Commit: "0123456789abcdef", Go: "go1.24", Platform: "linux/amd64"}, "hsh v1.2.3 (commit 0123456789ab, go1.24, linux/amd64)"},
		{Info{Commit: "abc", Modified: true, Go: "go1.24", Platform: "linux/arm64"}, "hsh dev (commit abc-dirty, go1.24, linux/arm64)"},
		{Info{Go: "go1.24", Platform: "linux/arm64"}, "hsh dev (go1.24, linux/arm64)"},
	}
	for _, c := range cases {
		if got := c.in.String(); got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
}
