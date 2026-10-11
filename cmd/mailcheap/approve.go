package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"mailcheap/internal/app"
	"mailcheap/internal/jobs"
	"mailcheap/internal/store"
)

// approve is the human gate of design §11: with SEND_APPROVAL=manual a send lands as a job waiting
// for somebody to release it, and this is that release.
func newApproveCmd() *cobra.Command {
	approve := &cobra.Command{
		Use:   "approve <job-id>",
		Short: "Release a job that is waiting for approval",
		Long: "Only needed with SEND_APPROVAL=manual. The job runs on the server that owns the queue, " +
			"so a serve process has to be up for the send to start.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApp(cmd, func(ctx context.Context, a *app.App) error {
				id := args[0]
				released, err := a.Jobs.Approve(ctx, id, time.Now().UTC())
				if err != nil {
					return err
				}
				if !released {
					// Say what the job is doing instead, which is the difference between "already
					// approved" and "the wrong id".
					job, err := a.Jobs.Get(ctx, id)
					if errors.Is(err, store.ErrNotFound) {
						return fmt.Errorf("no job %s", id)
					}
					if err != nil {
						return err
					}
					return fmt.Errorf("job %s is %s, not waiting for approval", id, job.State)
				}
				job, err := a.Jobs.Get(ctx, id)
				if err != nil {
					return err
				}
				note(cmd, a, "job.approve", id, map[string]any{"id": id, "kind": job.Kind})
				if jsonOutput(cmd) {
					return printJSON(cmd, job)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "released %s %s\n", job.Kind, id)
				return nil
			})
		},
	}
	return approve
}

var _ = jobs.AwaitingApproval
