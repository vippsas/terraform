package secureazurerm

import "github.com/hashicorp/terraform/backend"

// CLIInit sets the CLI options.
func (b *Backend) CLIInit(opts *backend.CLIOpts) error {
	b.props.ContextOpts = opts.ContextOpts
	return nil
}
