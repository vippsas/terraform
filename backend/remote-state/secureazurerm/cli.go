package secureazurerm

import (
	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/terraform"
	"github.com/mitchellh/cli"
	"github.com/mitchellh/colorstring"
)

// CLI shit.
type CLI struct {
	CLI         cli.Ui
	CLIColor    *colorstring.Colorize
	ContextOpts *terraform.ContextOpts
	// never ask for input. always validate. always run in automation.
}

// CLIInit inits CLI.
func (b *Backend) CLIInit(opts *backend.CLIOpts) error {
	b.cli.CLI = opts.CLI                 // neckbeard cli.
	b.cli.CLIColor = opts.CLIColor       // i <3 my colors.
	b.cli.ContextOpts = opts.ContextOpts // muh context.
	return nil
}

// Colorize makes the CLI output colored text.
func (cli *CLI) Colorize() *colorstring.Colorize {
	if cli.CLIColor != nil {
		return cli.CLIColor
	}
	return &colorstring.Colorize{
		Colors:  colorstring.DefaultColors,
		Disable: false, // ofc, we want color.
	}
}

func (cli *CLI) Warn(msg string) {
}

func (cli *CLI) Error(msg string) {
}
