// Package prompts embeds reviewed prompts in the binary while keeping them readable in Git.
package prompts

import _ "embed"

//go:embed agent.system.md
var AgentSystem string

//go:embed roast.system.md
var RoastSystem string

//go:embed group_context.md
var GroupContext string

//go:embed message_context.md
var MessageContextSystem string
