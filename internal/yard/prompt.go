package yard

import (
	"fmt"
	"strings"

	"github.com/daniil4545/famulus/internal/config"
	"github.com/daniil4545/famulus/internal/github"
	"github.com/daniil4545/famulus/internal/state"
	agentrules "github.com/daniil4545/famulus/rules"
)

var rules = strings.TrimSpace(agentrules.Text)

const ending = `## How this run ends

- Work in the current directory: the branch is checked out already. Commit your work there.
  Do not push and do not open a pull request: the runner does both.
- Your final message is posted to GitHub as it is, in Markdown.
  - With new commits it becomes the pull request description or a reply on the pull request.
  - Without commits it becomes a comment on the issue. Use that to ask the maintainer what
    you need when the issue does not give enough to do the work, under a ` + "`## Questions`" + ` heading.
  - A run without commits and without ` + "`## Questions`" + ` counts as unfinished: you get it back.`

// nudgePrompt goes back to a session that ended its turn with no commits and no questions.
const nudgePrompt = "You ended your turn before the task was done: no commits and no `## Questions`. " +
	"Continue from where you stopped. Finish with commits and the final message in the form the rules give, " +
	"or, if you are blocked, with a `## Questions` section and no commits."

func issuePrompt(repo config.Repo, issue github.Issue, notes []github.Note, branch string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n# Task\n\nRepository %s, base branch `%s`, your branch `%s`.\n\n", rules, repo.Name, repo.Base, branch)
	fmt.Fprintf(&b, "## Issue #%d: %s\n\n%s\n", issue.Number, issue.Title, strings.TrimSpace(issue.Body))
	if len(notes) > 0 {
		b.WriteString("\n## Comments of the maintainer on the issue\n\n")
		writeNotes(&b, notes)
	}
	b.WriteString("\n" + ending + "\n")
	return b.String()
}

func answerPrompt(t *state.Task, notes []github.Note) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The maintainer answered on issue #%d:\n\n", t.Issue)
	writeNotes(&b, notes)
	b.WriteString("\nContinue the task. " + ending + "\n")
	return b.String()
}

func reviewPrompt(t *state.Task, notes []github.Note) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The maintainer reviewed pull request #%d for issue #%d:\n\n", t.PR, t.Issue)
	writeNotes(&b, notes)
	b.WriteString("\nAddress every note: change the code and commit where needed. Your final message is " +
		"posted as a reply on the pull request: answer each note, and ask when a note is unclear.\n\n" + ending + "\n")
	return b.String()
}

// restartPrompt gives a new session the context an old session had, when there is none to resume.
func restartPrompt(repo config.Repo, t *state.Task) string {
	s := fmt.Sprintf("# Task\n\nRepository %s, base branch `%s`, your branch `%s`. You continue work on issue #%d: %s. "+
		"Read the branch history (`git log origin/%s..HEAD`) to see what is done already.",
		repo.Name, repo.Base, t.Branch, t.Issue, t.Title, repo.Base)
	if t.PR != 0 {
		s += fmt.Sprintf(" Pull request #%d is open for it.", t.PR)
	}
	return s
}

func writeNotes(b *strings.Builder, notes []github.Note) {
	for _, n := range notes {
		where := n.Kind
		if n.Path != "" {
			where += fmt.Sprintf(" on %s:%d", n.Path, n.Line)
		}
		fmt.Fprintf(b, "- %s (%s):\n\n%s\n\n", where, n.Created.Format("2006-01-02 15:04"), indent(strings.TrimSpace(n.Body)))
	}
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}
