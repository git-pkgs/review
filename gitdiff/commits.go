package gitdiff

import (
	"context"
	"strconv"
	"strings"

	"github.com/git-pkgs/review"
)

// maxCommits caps how much history is read for a long-lived branch.
const maxCommits = 500

// logFields is SHA, subject, body and the numstat block.
const logFields = 4

// commits lists the non-merge commits reachable from head but not base,
// oldest first, with the paths each one touched and by how much.
func (g gitRunner) commits(ctx context.Context, base, head string) ([]review.Commit, error) {
	out, err := g.run(ctx, "-c", "core.quotepath=off", "log", "-z", "--reverse", "--no-merges",
		"--max-count="+strconv.Itoa(maxCommits), "--no-renames", "--format=%x1e%H%x1f%s%x1f%b%x1f", "--numstat", base+".."+head, "--")
	if err != nil {
		return nil, err
	}
	return parseCommits(string(out)), nil
}

func parseCommits(out string) []review.Commit {
	var commits []review.Commit
	for record := range strings.SplitSeq(out, "\x1e") {
		fields := strings.SplitN(record, "\x1f", logFields)
		if len(fields) < logFields {
			continue
		}
		c := review.Commit{SHA: fields[0], Subject: fields[1], Body: strings.TrimSpace(fields[2])}
		for rec := range strings.SplitSeq(fields[3], "\x00") {
			parts := strings.SplitN(strings.TrimLeft(rec, "\n"), "\t", numstatFields)
			if len(parts) != numstatFields || parts[2] == "" {
				continue
			}
			add, _ := strconv.Atoi(parts[0])
			del, _ := strconv.Atoi(parts[1])
			c.Files = append(c.Files, review.CommitFile{Path: parts[2], Additions: add, Deletions: del})
		}
		commits = append(commits, c)
	}
	return commits
}

// branch names the head when it is a branch, or the current branch when the
// head is HEAD.
func (g gitRunner) branch(ctx context.Context, head string) string {
	if head == headRef {
		out, err := g.run(ctx, "symbolic-ref", "--quiet", "--short", headRef)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	if _, err := g.run(ctx, "show-ref", "--verify", "--quiet", "refs/heads/"+head); err == nil {
		return head
	}
	return ""
}
