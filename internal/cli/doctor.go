package cli

import (
	"fmt"
	"net/http"

	"git.host.bzh/pepe/kuberpack/internal/api"
	"github.com/spf13/cobra"
)

// doctorCommand reports what the platform can actually do. It answers,
// before a push, the questions that otherwise cost a compilation, a
// webhook and a log dig: can the control plane read namespaces, are the
// addon operators declared, which certificate mode is in force, where is
// the chart, is the secrets boundary configured.
func (a *App) doctorCommand(root *cobra.Command) {
	root.AddCommand(&cobra.Command{
		Use:   "doctor",
		Short: "Preflight the platform: what it can and cannot do",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			c, _, err := a.connect()
			if err != nil {
				return err
			}
			var report api.Doctor
			if err := c.json(a.ctx, http.MethodGet, "/api/v2/doctor", nil, &report); err != nil {
				return err
			}
			if a.output == "json" {
				return a.print(report)
			}
			failed := 0
			for _, check := range report.Checks {
				mark := "✓"
				if !check.OK {
					mark = "✗"
					failed++
				}
				fmt.Fprintf(a.Out, "%s %-18s %s\n", mark, check.Name, check.Detail)
			}
			if failed > 0 {
				return fail(1, "%d check(s) failed", failed)
			}
			return nil
		},
	})
}
