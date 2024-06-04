package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestIsDebugMode(t *testing.T) {
	rootConfig.Debug = true
	if !IsDebugMode() {
		t.Errorf("IsDebugMode() = %v; want true", IsDebugMode())
	}

	rootConfig.Debug = false
	if IsDebugMode() {
		t.Errorf("IsDebugMode() = %v; want false", IsDebugMode())
	}
}

func TestRootCmd(t *testing.T) {
	if RootCmd.Use != "tfe-plan-bot" {
		t.Errorf("RootCmd.Use = %v; want 'tfe-plan-bot'", RootCmd.Use)
	}

	if RootCmd.Short != "A bot for creating TFE speculative plans on Github pull requests." {
		t.Errorf("RootCmd.Short = %v; want 'A bot for creating TFE speculative plans on Github pull requests.'", RootCmd.Short)
	}

	if RootCmd.Long != "A bot for creating TFE speculative plans on Github pull requests, when a given policy is satisfied." {
		t.Errorf("RootCmd.Long = %v; want 'A bot for creating TFE speculative plans on Github pull requests, when a given policy is satisfied.'", RootCmd.Long)
	}

	if RootCmd.SilenceUsage != true {
		t.Errorf("RootCmd.SilenceUsage = %v; want true", RootCmd.SilenceUsage)
	}
}

func TestRootCmdDebugFlag(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().AddFlagSet(RootCmd.PersistentFlags())

	if cmd.Flag("debug") == nil {
		t.Errorf("Expected debug flag to be defined")
	}
}

func TestInit(t *testing.T) {
	cmd := &cobra.Command{
		Use: "tfe-plan-bot",
	}
	cmd.PersistentFlags().BoolVarP(&rootConfig.Debug, "debug", "d", false, "enables debug output")

	if cmd.Flag("debug") == nil {
		t.Errorf("Expected debug flag to be defined")
	}

	if cmd.Flag("debug").Shorthand != "d" {
		t.Errorf("Expected debug flag shorthand to be 'd'")
	}

	if cmd.Flag("debug").Usage != "enables debug output" {
		t.Errorf("Expected debug flag usage to be 'enables debug output'")
	}

	if cmd.Flag("debug").DefValue != "false" {
		t.Errorf("Expected debug flag default value to be 'false'")
	}
}