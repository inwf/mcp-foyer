package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/spf13/cobra"

	"mcphub/internal/config"
	"mcphub/internal/logging"
)

func newCheckCommand(global *globalOptions, stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Validate the configuration and report where everything lives",
		Long: "Perform the startup sequence up to the point of listening: resolve the\n" +
			"paths, load and validate the configuration, open the log destinations,\n" +
			"and report the result. Nothing is served and no port is bound.",
		Args: noPositionalArgs,
		RunE: func(*cobra.Command, []string) error {
			return selfCheck(global.DataDir, global.ConfigPath, stdout)
		},
	}
}

// selfCheck performs the startup sequence up to the point of listening:
// resolve where everything lives, load and validate the configuration,
// open the log destinations, and report the result.
func selfCheck(dataDirFlag, configFlag string, stdout io.Writer) error {
	paths, err := config.ResolveDataDir(dataDirFlag)
	if err != nil {
		return err
	}
	if err := paths.Ensure(); err != nil {
		return err
	}

	cfgPath, err := resolveConfigPath(paths, configFlag)
	if err != nil {
		return err
	}

	cfg, err := config.Load(cfgPath)
	usingDefaults := errors.Is(err, fs.ErrNotExist)
	switch {
	case usingDefaults:
		cfg = config.Default()
	case err != nil:
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("%s: %w", cfgPath, err)
	}

	store := logging.NewStore(logging.DefaultCapacity)
	opts, err := logging.OptionsFrom(cfg.Logging, paths, store)
	if err != nil {
		return err
	}
	opts.Stdout = nil // the report below is this command's only output
	log, err := logging.New(opts)
	if err != nil {
		return err
	}
	defer log.Close()

	log.For(logging.ModuleCLI).Info("startup self-check", "version", version)

	report(stdout, paths, cfgPath, usingDefaults, cfg)
	return nil
}

func report(w io.Writer, paths config.Paths, cfgPath string, usingDefaults bool, cfg config.Config) {
	configNote := ""
	if usingDefaults {
		configNote = "  (not found, using defaults)"
	}

	enabled := 0
	for _, server := range cfg.MCPServers {
		if server.Enabled {
			enabled++
		}
	}

	fmt.Fprintf(w, "mcphub %s\n\n", version)

	fmt.Fprintf(w, "data dir:   %s\n", paths.Root())
	fmt.Fprintf(w, "config:     %s%s\n", cfgPath, configNote)
	fmt.Fprintf(w, "log dir:    %s\n", paths.LogDir())
	fmt.Fprintf(w, "log file:   %s\n", paths.LogFile())
	fmt.Fprintf(w, "usage:      %s\n", paths.UsageFile())

	fmt.Fprintf(w, "\nlisten:     %s:%d\n", cfg.Listen.Host, cfg.Listen.Port)
	fmt.Fprintf(w, "log level:  %s\n", cfg.Logging.Level)
	fmt.Fprintf(w, "session:    %s\n", cfg.Gateway.DefaultSessionMode)
	fmt.Fprintf(w, "servers:    %d configured, %d enabled\n", len(cfg.MCPServers), enabled)
}
