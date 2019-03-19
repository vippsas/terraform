package secureazurerm

import (
	"github.com/hashicorp/terraform/backend"
	"github.com/mitchellh/colorstring"
)

// CLIInit inits CLI.
func (b *Backend) CLIInit(opts *backend.CLIOpts) error {
	b.CLI = opts.CLI                 // neckbeard cli.
	b.CLIColor = opts.CLIColor       // i <3 my colors.
	b.ContextOpts = opts.ContextOpts // muh context.

	b.OpInput = opts.Input           // i want user input or not.
	b.OpValidation = opts.Validation // i want to validate or not.

	b.RunningInAutomation = opts.RunningInAutomation // i am running in automation tool with no user interaction or not.
	return nil
}

// Colorize makes the CLI output colored text.
func (b *Backend) Colorize() *colorstring.Colorize {
	if b.CLIColor != nil {
		return b.CLIColor
	}
	return &colorstring.Colorize{
		Colors:  colorstring.DefaultColors,
		Disable: false, // ofc, we want color.
	}
}
