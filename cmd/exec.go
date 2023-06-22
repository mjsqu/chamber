package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	analytics "github.com/segmentio/analytics-go/v3"
	"github.com/segmentio/chamber/v2/environ"
	"github.com/spf13/cobra"
)

// When true, only use variables retrieved from the backend, do not inherit existing environment variables
var pristine bool

// When true, enable strict mode, which checks that all secrets replace env vars with a special sentinel value
var strict bool

// Value to expect in strict mode
var strictValue string

// Default value to expect in strict mode
const strictValueDefault = "chamberme"

// execCmd represents the exec command
var execCmd = &cobra.Command{
	Use:   "exec <service...> -- <command> [<arg...>]",
	Short: "Executes a command with secrets loaded into the environment",
	Args: func(cmd *cobra.Command, args []string) error {
		dashIx := cmd.ArgsLenAtDash()
		if dashIx == -1 {
			return errors.New("please separate services and command with '--'. See usage")
		}
		if err := cobra.MinimumNArgs(1)(cmd, args[:dashIx]); err != nil {
			return fmt.Errorf("at least one service must be specified: %w", err)
		}
		if err := cobra.MinimumNArgs(1)(cmd, args[dashIx:]); err != nil {
			return fmt.Errorf("must specify command to run. See usage: %w", err)
		}
		return nil
	},
	RunE: execRun,
	Example: `
Given a secret store like this:

	$ echo '{"db_username": "root", "db_password": "hunter22"}' | chamber import - 

--strict will fail with unfilled env vars

	$ HOME=/tmp DB_USERNAME=chamberme DB_PASSWORD=chamberme EXTRA=chamberme chamber exec --strict service exec -- env
	chamber: extra unfilled env var EXTRA
	exit 1

--pristine takes effect after checking for --strict values

	$ HOME=/tmp DB_USERNAME=chamberme DB_PASSWORD=chamberme chamber exec --strict --pristine service exec -- env
	DB_USERNAME=root
	DB_PASSWORD=hunter22

--noclobber does not overwrite existing environment variables

	$ HOME=/tmp DB_USERNAME=bert chamber exec --noclobber service exec -- env
	DB_USERNAME=bert
	DB_PASSWORD=hunter22
`,
}

func init() {
	execCmd.Flags().BoolVar(&pristine, "pristine", false, "only use variables retrieved from the backend; do not inherit existing environment variables")
	execCmd.Flags().BoolVar(&strict, "strict", false, `enable strict mode:
only inject secrets for which there is a corresponding env var with value
<strict-value>, and fail if there are any env vars with that value missing
from secrets`)
	execCmd.Flags().BoolVar(&noclobber, "noclobber", false, "inherit existing environment variables; do not overwrite with variables retrieved from backend")
	execCmd.Flags().StringVar(&strictValue, "strict-value", strictValueDefault, "value to expect in --strict mode")
	RootCmd.AddCommand(execCmd)
}

func execRun(cmd *cobra.Command, args []string) error {
	dashIx := cmd.ArgsLenAtDash()
	services, command, commandArgs := args[:dashIx], args[dashIx], args[dashIx+1:]

	if analyticsEnabled && analyticsClient != nil {
		analyticsClient.Enqueue(analytics.Track{
			UserId: username,
			Event:  "Ran Command",
			Properties: analytics.NewProperties().
				Set("command", "exec").
				Set("chamber-version", chamberVersion).
				Set("services", services).
				Set("backend", backend),
		})
	}

	for _, service := range services {
		if err := validateServiceWithLabel(service); err != nil {
			return fmt.Errorf("Failed to validate service: %w", err)
		}
	}

	secretStore, err := getSecretStore()
	if err != nil {
		return fmt.Errorf("Failed to get secret store: %w", err)
	}
	_, noPaths := os.LookupEnv("CHAMBER_NO_PATHS")

	if pristine && verbose {
		fmt.Fprintf(os.Stderr, "chamber: pristine mode engaged\n")
	}

	var env environ.Environ
	if strict {
		if verbose {
			fmt.Fprintf(os.Stderr, "chamber: strict mode engaged\n")
		}
		var err error
		env = environ.Environ(os.Environ())
		if noPaths {
			err = env.LoadStrictNoPaths(secretStore, strictValue, pristine, services...)
		} else {
			err = env.LoadStrict(secretStore, strictValue, pristine, services...)
		}
		if err != nil {
			return err
		}
	} else {
		if !pristine {
			env = environ.Environ(os.Environ())
		}
		for _, service := range services {
			collisions := make([]string, 0)
			var err error
			// TODO: these interfaces should look the same as Strict*, so move pristine in there
			if noPaths {
				err = env.LoadNoPaths(secretStore, service, &collisions)
			} else {
				if noclobber {
					err = env.loadNoClobber(secretStore, service, &collisions, false)
				}
				else {
					err = env.Load(secretStore, service, &collisions)
				}
			}
			if err != nil {
				return fmt.Errorf("Failed to list store contents: %w", err)
			}

			for _, c := range collisions {
				if noclobber {
					fmt.Fprintf(os.Stderr, "warning: Not overwriting existing environment variable %s from service %s\n", c, service)
				}
				else {
					fmt.Fprintf(os.Stderr, "warning: service %s overwriting environment variable %s\n", service, c)
				}
			}
		}
	}

	if verbose {
		fmt.Fprintf(os.Stdout, "info: With environment %s\n", strings.Join(env, ","))
	}

	return exec(command, commandArgs, env)
}
