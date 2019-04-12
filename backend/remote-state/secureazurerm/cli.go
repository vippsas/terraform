package secureazurerm

import (
	"github.com/hashicorp/terraform/backend"
	"github.com/mitchellh/colorstring"
)

// CLIInit inits CLI.
func (b *Backend) CLIInit(opts *backend.CLIOpts) error {
	b.CLI = opts.CLI
	b.CLIColor = opts.CLIColor
	b.ContextOpts = opts.ContextOpts
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

// OutputColor outputs the setup colored text to the terminal.
func (b *Backend) OutputColor(message string) {
	b.CLI.Output(b.Colorize().Color(message))
}
