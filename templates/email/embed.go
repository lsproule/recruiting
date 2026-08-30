// Package email embeds the outbound email templates so the binary carries
// them and no deployment has to ship a templates directory.
//
// Each template is three files sharing a name: `<name>.subject.tmpl`,
// `<name>.txt.tmpl`, and `<name>.html.tmpl`. Every one of them may use
// `.OrgName` plus whatever the job payload's data carries.
package email

import "embed"

//go:embed *.tmpl
var FS embed.FS
