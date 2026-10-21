package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"sierra-rollout/internal/spec"
	"sierra-rollout/internal/workflows"
)

const watchInterval = 2 * time.Second

// Exit codes.
const (
	ExitOK       = 0
	ExitError    = 1 // server or transport error
	ExitUsage    = 2 // policy diagnostics or usage error
	ExitConflict = 3 // 409
)

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// ExitCode maps a command error to the process exit code.
func ExitCode(err error) int {
	var ee *exitError
	switch {
	case err == nil:
		return ExitOK
	case errors.As(err, &ee):
		return ee.code
	case errors.Is(err, ErrConflict):
		return ExitConflict
	}
	return ExitError
}

func NewRootCmd() *cobra.Command {
	server := os.Getenv("ROLLOUT_SERVER")
	if server == "" {
		server = "http://127.0.0.1:8080"
	}
	root := &cobra.Command{Use: "rolloutctl", Short: "Plan, start and operate agent rollouts", SilenceUsage: true}
	root.PersistentFlags().StringVar(&server, "server", server, "rollout API address (env ROLLOUT_SERVER)")
	client := func() *Client { return &Client{BaseURL: server} }

	root.AddCommand(policyCmd("plan", "Compile a policy and print the plan", func(cmd *cobra.Command, file string, policy []byte, version string) error {
		p, diags, err := client().Plan(cmd.Context(), policy, version)
		if err := diagnose(cmd, file, diags, err); err != nil {
			return err
		}
		RenderPlan(cmd.OutOrStdout(), p)
		return nil
	}))
	root.AddCommand(policyCmd("start", "Compile a policy and start the rollout", func(cmd *cobra.Command, file string, policy []byte, version string) error {
		id, diags, err := client().Start(cmd.Context(), policy, version)
		if err := diagnose(cmd, file, diags, err); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), id)
		return nil
	}))

	var watch bool
	status := &cobra.Command{
		Use: "status <rollout-id>", Short: "Show rollout status", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for {
				st, err := client().Status(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				if watch {
					fmt.Fprint(cmd.OutOrStdout(), "\033[H\033[2J")
				}
				RenderStatus(cmd.OutOrStdout(), st)
				if !watch || terminal(st.State) {
					return nil
				}
				time.Sleep(watchInterval)
			}
		},
	}
	status.Flags().BoolVar(&watch, "watch", false, "refresh every 2s until the rollout finishes")
	root.AddCommand(status)

	for _, sig := range []string{workflows.SignalApprove, workflows.SignalReject, workflows.SignalResume, workflows.SignalRetry, workflows.SignalAbort} {
		root.AddCommand(signalCmd(sig, client))
	}
	return root
}

func policyCmd(name, short string, run func(cmd *cobra.Command, file string, policy []byte, version string) error) *cobra.Command {
	var file, version string
	cmd := &cobra.Command{
		Use: name, Short: short, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			policy, err := os.ReadFile(file)
			if err != nil {
				return &exitError{ExitUsage, err}
			}
			return run(cmd, file, policy, version)
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "policy YAML file")
	cmd.Flags().StringVar(&version, "version", "", "version to deploy")
	_ = cmd.MarkFlagRequired("file")
	_ = cmd.MarkFlagRequired("version")
	return cmd
}

func signalCmd(sig string, client func() *Client) *cobra.Command {
	var target, validation string
	cmd := &cobra.Command{
		Use: sig + " <rollout-id>", Args: cobra.ExactArgs(1),
		Short: fmt.Sprintf("Send %s to a target", sig),
		RunE: func(cmd *cobra.Command, args []string) error {
			if sig == workflows.SignalAbort && target == "" {
				return client().Abort(cmd.Context(), args[0])
			}
			if target == "" {
				return &exitError{ExitUsage, errors.New("--target is required")}
			}
			return client().Signal(cmd.Context(), args[0], target, sig, validation)
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "target ID, e.g. prod/canary/acme/us-east-1")
	if sig == workflows.SignalApprove || sig == workflows.SignalReject {
		cmd.Flags().StringVar(&validation, "validation", "", "validation to approve/reject (default: all awaiting)")
	}
	if sig == workflows.SignalAbort {
		cmd.Short = "Abort the rollout, or a paused target with --target"
	}
	return cmd
}

func diagnose(cmd *cobra.Command, file string, diags []spec.Diagnostic, err error) error {
	if err != nil {
		return err
	}
	if len(diags) > 0 {
		RenderDiagnostics(cmd.ErrOrStderr(), file, diags)
		return &exitError{ExitUsage, fmt.Errorf("%d policy errors", len(diags))}
	}
	return nil
}

func terminal(state string) bool {
	return state == workflows.Succeeded || state == workflows.Held || state == workflows.Aborted
}
