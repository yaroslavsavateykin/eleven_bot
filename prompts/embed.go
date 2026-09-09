// Package prompts embeds reviewed prompts in the binary while keeping them readable in Git.
package prompts

import _ "embed"

//go:embed agent.system.md
var AgentSystem string

//go:embed ask.system.md
var AskSystem string

//go:embed event.system.md
var EventSystem string

//go:embed event_response_format.md
var EventResponseFormat string

//go:embed roast.system.md
var RoastSystem string

//go:embed group_context.md
var GroupContext string

//go:embed week_parity_context.md
var WeekParityContext string

//go:embed week_parity_missing.md
var WeekParityMissing string
