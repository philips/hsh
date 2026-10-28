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

func updateCommand() *ff.Command {
	var pinTag string
	var noMetrics bool
	return newCommand("update", "hsh update [--version vTAG] [--no-metrics]", "update hsh to the latest release from GitHub", func(fs *ff.FlagSet) {
		fs.StringVar(&pinTag, 0, "version", "", "update to a specific tag (e.g. v1.2.3) instead of latest")
		fs.BoolVar(&noMetrics, 0, "no-metrics", "skip the anonymous usage metrics report")
	}, func(ctx context.Context, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("update takes no arguments")
		}
		if os.Getenv("HSH_NO_METRICS") == "1" {
			noMetrics = true
		}
		base := os.Getenv("HSH_RELEASE_BASE")
		if base == "" {
			base = update.DefaultReleaseBase
		}
		metricsURL := os.Getenv("HSH_METRICS_URL")
		if metricsURL == "" {
			metricsURL = update.DefaultMetricsURL
		}
		tag := pinTag
		if tag == "" {
			tag = os.Getenv("HSH_VERSION")
		}
		if !noMetrics {
			fmt.Printf(metricsNoticeFormat, runtime.GOOS+"/"+runtime.GOARCH)
		}
		if tag == "" {
			var err error
			tag, err = update.Available(base)
			if err != nil {
				return err
			}
		}
		current := version.Get()
		if tag == current.Tag && current.Tag != "" {
			fmt.Printf("hsh %s is up to date\n", tag)
			return nil
		}
		fmt.Printf("updating hsh %s -> %s...\n", currentDisplay(current), tag)
		if _, err := update.Do(update.Options{BaseURL: base, Tag: tag}); err != nil {
			return err
		}
		reportVersion := current.Tag
		if reportVersion == "" {
			reportVersion = "dev"
		}
		if !noMetrics {
			sessions, _ := update.Take(update.DefaultPath())
			if err := update.Report(metricsURL, reportVersion, sessions); err != nil {
				infof("hsh: warning: %v\n", err)
			}
		} else {
			update.Take(update.DefaultPath())
		}
		fmt.Printf("✓ hsh %s installed (was %s)\n", tag, currentDisplay(current))
		return nil
	})
}

func currentDisplay(info version.Info) string {
	if info.Tag == "" {
		return "dev"
	}
	return info.Tag
}
