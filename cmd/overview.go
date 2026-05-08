package cmd

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/buildoor/pkg/webui"
)

var overviewCmd = &cobra.Command{
	Use:   "overview",
	Short: "Serve a multi-instance overview UI",
	Long: `Starts a tiny HTTP server that aggregates a high-level view of one or
more buildoor instances (online status, ePBS/Builder API mode, balance,
recent build stats, EL client type).

Hosts can be supplied either as multiple --host flags or as a comma-separated
list. Examples:

  buildoor overview --host http://a:8082 --host http://b:8082
  buildoor overview --host http://a:8082,http://b:8082

Each --host must point at a buildoor instance with --api-port enabled.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		hostFlag, err := cmd.Flags().GetStringSlice("host")
		if err != nil {
			return err
		}

		port, err := cmd.Flags().GetInt("port")
		if err != nil {
			return err
		}

		bind, err := cmd.Flags().GetString("bind")
		if err != nil {
			return err
		}

		hosts := normalizeOverviewHosts(hostFlag)

		return webui.StartOverviewServer(&webui.OverviewConfig{
			Host:           bind,
			Port:           port,
			Hosts:          hosts,
			InjectHeadHTML: cfg.InjectHeadHTML,
		}, logger)
	},
}

// normalizeOverviewHosts flattens a list of --host values, splitting on commas
// and trimming whitespace, so users can mix `--host a,b` with `--host c`.
func normalizeOverviewHosts(in []string) []string {
	out := make([]string, 0, len(in))

	for _, raw := range in {
		for part := range strings.SplitSeq(raw, ",") {
			h := strings.TrimSpace(part)
			if h != "" {
				out = append(out, h)
			}
		}
	}

	return out
}

func init() {
	overviewCmd.Flags().StringSlice("host", nil, "buildoor instance URL (repeatable; comma-separated values supported)")
	overviewCmd.Flags().Int("port", 8090, "HTTP port for the overview UI")
	overviewCmd.Flags().String("bind", "0.0.0.0", "Bind address for the overview UI")

	if err := overviewCmd.MarkFlagRequired("host"); err != nil {
		panic(err)
	}

	rootCmd.AddCommand(overviewCmd)
}
