// Package rules embeds the rules that every agent run starts with.
package rules

import _ "embed"

//go:embed AGENTS.md
var Text string

// Review is what the review agent starts with: the checklist and the answer format.
//
//go:embed REVIEW.md
var Review string

// Principles are the owner's design rules for every project; they follow the checklist in
// the review prompt.
//
//go:embed PRINCIPLES.md
var Principles string
