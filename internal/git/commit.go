package git

import "time"

// Commit is a single commit in the range being recomposed.
type Commit struct {
	SHA     string // full 40-char sha
	Short   string // abbreviated sha (7-12 chars, depending on git config)
	Author  string
	Email   string
	Date    time.Time
	Subject string
	Body    string // raw body, no subject line
	Files   []FileStat
}

// FileStat is one entry from `git show --name-status`.
type FileStat struct {
	Status string // M, A, D, R, C, T, U, ...
	Path   string
}

// Message returns the full commit message (subject + blank line + body).
func (c Commit) Message() string {
	if c.Body == "" {
		return c.Subject
	}
	return c.Subject + "\n\n" + c.Body
}
