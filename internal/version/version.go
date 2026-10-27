package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

var Tag = ""

type Info struct {
	Tag      string
	Commit   string
	Modified bool
	Go       string
	Platform string
}

func Get() Info {
	info := Info{Tag: Tag, Go: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.Commit = s.Value
		case "vcs.modified":
			info.Modified = s.Value == "true"
		}
	}
	if info.Tag == "" && info.Commit == "" && bi.Main.Version != "(devel)" {
		info.Tag = bi.Main.Version
	}
	return info
}

func (i Info) String() string {
	tag := i.Tag
	if tag == "" {
		tag = "dev"
	}
	var details []string
	if i.Commit != "" {
		c := i.Commit
		if len(c) > 12 {
			c = c[:12]
		}
		if i.Modified {
			c += "-dirty"
		}
		details = append(details, "commit "+c)
	}
	details = append(details, i.Go, i.Platform)
	return fmt.Sprintf("hsh %s (%s)", tag, strings.Join(details, ", "))
}
