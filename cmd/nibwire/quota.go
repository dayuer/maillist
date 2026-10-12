package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"nibwire/internal/app"
	"nibwire/internal/core/ratectl"
	"nibwire/internal/core/stats"
)

// newQuotaCmd is `nibwire quota`: what the account allows, and what this deployment would send at.
//
// Both halves matter and neither is guessable from the other. The account's numbers are what SES
// enforces; the rate is what this process aims at, which is those numbers less the headroom, less any
// backoff a throttle has caused. "Why is it sending slowly?" is answered here and nowhere else.
func newQuotaCmd() *cobra.Command {
	quota := &cobra.Command{
		Use:   "quota",
		Short: "Show the account's sending limits and the rate this deployment sends at",
		Long: "Reads the sending quota from SES (GetAccount) and prints it next to the rate this " +
			"process would use. The rate follows the account's own numbers, and it backs off when " +
			"SES refuses a send for being too fast.\n\n" +
			"This needs the ses:GetAccount permission. Without it the deployment is told the account " +
			"cannot be reached and sends one email a second—slowly, and with an alert saying so.",
		Args: cobra.NoArgs,
		RunE: runQuota,
	}
	return quota
}

func runQuota(cmd *cobra.Command, _ []string) error {
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		reading, err := a.ReadQuota(ctx)
		if err != nil {
			return fmt.Errorf("could not read the SES sending quota: %w", err)
		}
		rate := a.Mailer.SendingRate()
		account := accountLine(reading)
		remaining := describeRemaining(rate)

		if jsonOutput(cmd) {
			return printJSON(cmd, map[string]any{
				"account": map[string]any{
					"maxSendRate":     reading.MaxSendRate,
					"max24HourSend":   reading.Max24HourSend,
					"sentLast24Hours": reading.SentLast24Hours,
					"readAt":          reading.ReadAt,
				},
				"sending":   rate,
				"remaining": remaining,
			})
		}
		out := cmd.OutOrStdout()
		_, _ = fmt.Fprintf(out, "account       %s\n", account)
		_, _ = fmt.Fprintf(out, "sending       %s\n", describeRate(rate))
		_, _ = fmt.Fprintf(out, "24h remaining %s\n", remaining)
		// What the rate means for a real send, because "450/s" is not a number anyone can feel.
		_, _ = fmt.Fprintf(out, "estimated     %s for 1,000 messages\n",
			rate.Estimate(1000).Round(time.Second))
		return nil
	})
}

// accountLine is the account's own numbers, in the units SES reports them.
func accountLine(reading ratectl.Quota) string {
	line := fmt.Sprintf("%v sends a second", trimRate(reading.MaxSendRate))
	switch {
	case reading.Max24HourSend < 0:
		return line + ", no 24-hour limit"
	case reading.Max24HourSend == 0:
		return line + ", and no 24-hour allowance at all"
	default:
		return fmt.Sprintf("%s, %s of %s sent in the last 24 hours", line,
			stats.FormatNumber(int(reading.SentLast24Hours)),
			stats.FormatNumber(int(reading.Max24HourSend)))
	}
}
