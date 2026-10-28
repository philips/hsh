package cli

import (
	"context"
	"fmt"
	"os"
	"runtime"

	"github.com/peterbourgon/ff/v4"

	"hsh/internal/update"
	"hsh/internal/version"
)

const metricsNoticeFormat = `hsh update reports anonymous usage metrics to the project:
version, platform (%s), and the number of sessions created since the
last update. No identifiable information is uploaded.
Skip with --no-metrics or HSH_NO_METRICS=1.
`

type updateRun struct {
	base      string
	metrics   string
	pin       string
	noMetrics bool
}

func updateRunFromEnv() updateRun {
	r := updateRun{
		base:    update.DefaultReleaseBase,
		metrics: update.DefaultMetricsURL,
	}
	if v := os.Getenv("HSH_RELEASE_BASE"); v != "" {
		r.base = v
	}
	if v := os.Getenv("HSH_METRICS_URL"); v != "" {
		r.metrics = v
	}
	if v := os.Getenv("HSH_VERSION"); v != "" {
		r.pin = v
	}
	if os.Getenv("HSH_NO_METRICS") == "1" {
		r.noMetrics = true
	}
	return r
}

func runUpdate(r updateRun) error {
	current := version.Get()
	if !r.noMetrics {
		fmt.Printf(metricsNoticeFormat, runtime.GOOS+"/"+runtime.GOARCH)
	}
	tag := r.pin
	if tag == "" {
		var err error
		tag, err = update.Available(r.base)
		if err != nil {
			return err
		}
	}
	if tag == current.Tag && current.Tag != "" {
		fmt.Printf("hsh %s is up to date\n", tag)
		return nil
	}
	fmt.Printf("updating hsh %s -> %s...\n", currentDisplay(current), tag)
	if _, err := update.Do(update.Options{BaseURL: r.base, Tag: tag}); err != nil {
		return err
	}
	reportVersion := current.Tag
	if reportVersion == "" {
		reportVersion = "dev"
	}
	if !r.noMetrics {
		sessions, _ := update.Take(update.DefaultPath())
		if err := update.Report(r.metrics, reportVersion, sessions); err != nil {
			infof("hsh: warning: %v\n", err)
		}
	} else {
		update.Take(update.DefaultPath())
	}
	if err := update.ClearLatest(update.DefaultPath()); err != nil {
		infof("hsh: warning: %v\n", err)
	}
	fmt.Printf("✓ hsh %s installed (was %s)\n", tag, currentDisplay(current))
	return nil
}

func currentDisplay(info version.Info) string {
	if info.Tag == "" {
		return "dev"
	}
	return info.Tag
}

func autoupdateCheck() error {
	r := updateRunFromEnv()
	path := update.DefaultPath()
	if err := update.MarkCheck(path); err != nil {
		infof("hsh: autoupdate: %v\n", err)
	}
	current := version.Get()
	tag, err := update.Available(r.base)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hsh: autoupdate check: %v\n", err)
		return nil
	}
	if err := update.SetLatest(path, tag); err != nil {
		infof("hsh: autoupdate: %v\n", err)
	}
	switch {
	case update.Newer(current.Tag, tag):
		if current.Tag == "" {
			fmt.Printf("hsh %s is available (this is a dev build): it installs on the next hsh command\n", tag)
		} else {
			fmt.Printf("hsh %s is available (this is %s): it installs on the next hsh command\n", tag, current.Tag)
		}
	default:
		fmt.Printf("hsh %s is up to date\n", tag)
	}
	return nil
}

func updateCommand() *ff.Command {
	var pinTag string
	var noMetrics, auto, noAuto, checkOnly bool
	return newCommand("update", "hsh update [--version vTAG] [--no-metrics] | hsh update [--auto|--no-auto] | hsh update --autoupdate-check", "update hsh to the latest release from GitHub", func(fs *ff.FlagSet) {
		fs.StringVar(&pinTag, 0, "version", "", "update to a specific tag (e.g. v1.2.3) instead of latest")
		fs.BoolVar(&noMetrics, 0, "no-metrics", "skip the anonymous usage metrics report")
		fs.BoolVar(&auto, 0, "auto", "enable automatic updates (background check, install on the next hsh command)")
		fs.BoolVar(&noAuto, 0, "no-auto", "disable automatic updates")
		fs.BoolVar(&checkOnly, 0, "autoupdate-check", "resolve the latest release and record it, without updating")
	}, func(ctx context.Context, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("update takes no arguments")
		}
		if auto || noAuto {
			if auto && noAuto {
				return fmt.Errorf("pass either --auto or --no-auto, not both")
			}
			if err := update.SetAuto(update.DefaultPath(), auto); err != nil {
				return err
			}
			if auto {
				fmt.Println("automatic updates enabled: hsh checks for new releases in the background and installs them on the next hsh command")
				fmt.Println("turn off with: hsh update --no-auto")
			} else {
				fmt.Println("automatic updates disabled")
			}
			return nil
		}
		if checkOnly {
			return autoupdateCheck()
		}
		r := updateRunFromEnv()
		if pinTag != "" {
			r.pin = pinTag
		}
		if noMetrics {
			r.noMetrics = true
		}
		return runUpdate(r)
	})
}
