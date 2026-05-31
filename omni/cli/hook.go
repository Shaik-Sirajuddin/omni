package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	codexconnector "github.com/Shaik-Sirajuddin/memory/connector/codeagent/codex"
	operatorimpl "github.com/Shaik-Sirajuddin/memory/operator/impl"
	hookoperator "github.com/Shaik-Sirajuddin/memory/svc/hook-operator"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func (c *DefaultCli) newHookCommand() *cobra.Command {
	var eventName string

	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Forward a codeagent hook event to the hook-operator",
		RunE: func(cmd *cobra.Command, args []string) error {
			eventName = strings.TrimSpace(eventName)
			if eventName == "" {
				return errors.New("--event is required")
			}
			var body []byte
			if in, ok := cmd.InOrStdin().(*os.File); !ok || !term.IsTerminal(int(in.Fd())) {
				var err error
				body, err = io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return fmt.Errorf("read hook payload: %w", err)
				}
			}
			if len(body) == 0 {
				body = []byte("{}")
			}
			result, err := operatorimpl.PostHookCallback(hookoperator.SocketPath(), operatorimpl.HookCallbackRequest{
				EventName: eventName,
				Body:      body,
			})
			if err != nil {
				return err
			}
			payload, err := codexconnector.MarshalHookOutput(codexconnector.HookOutput{
				Continue:       result.Continue,
				StopReason:     result.StopReason,
				SuppressOutput: result.SuppressOutput,
				SystemMessage:  result.SystemMessage,
			})
			if err != nil {
				return fmt.Errorf("marshal hook result: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(payload))
			return nil
		},
	}
	cmd.Flags().StringVar(&eventName, "event", "", "Hook event name")
	_ = cmd.MarkFlagRequired("event")
	return cmd
}

func (c *DefaultCli) newDoctorHooksCommand() *cobra.Command {
	hooksCmd := &cobra.Command{
		Use:   "hooks",
		Short: "Inspect hook-operator setup",
	}
	hooksCmd.AddCommand(c.newDoctorHooksStatusCommand())
	hooksCmd.AddCommand(c.newDoctorHooksRegistryCommand())
	return hooksCmd
}

func (c *DefaultCli) newDoctorHooksStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check hook-operator provider hook status",
		RunE: func(cmd *cobra.Command, args []string) error {
			statuses, err := operatorimpl.GetHookProviderStatuses(hookoperator.SocketPath())
			if err != nil {
				return err
			}
			if len(statuses) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no hook providers registered")
				return nil
			}
			allOK := true
			for _, status := range statuses {
				if !status.OK {
					allOK = false
					break
				}
			}
			if allOK {
				fmt.Fprintf(cmd.OutOrStdout(), "hooks ok (%d provider(s))\n", len(statuses))
				return nil
			}
			for _, status := range statuses {
				if status.OK {
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s missing: %s\n", status.Provider, strings.Join(status.Missing, ", "))
			}
			return nil
		},
	}
}

func (c *DefaultCli) newDoctorHooksRegistryCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "registry",
		Short: "Print hook-operator provider registry",
		RunE: func(cmd *cobra.Command, args []string) error {
			statuses, err := operatorimpl.GetHookProviderStatuses(hookoperator.SocketPath())
			if err != nil {
				return err
			}
			if len(statuses) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no hook providers registered")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "PROVIDER\tOK\tMISSING")
			for _, status := range statuses {
				missing := "-"
				if len(status.Missing) > 0 {
					missing = strings.Join(status.Missing, ",")
				}
				fmt.Fprintf(w, "%s\t%t\t%s\n", status.Provider, status.OK, missing)
			}
			return w.Flush()
		},
	}
}
