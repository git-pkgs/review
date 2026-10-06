package gitdiff

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"github.com/git-pkgs/review"
)

// diffFlags pins the diff options user config could otherwise change.
var diffFlags = []string{"--no-color", "--no-ext-diff", "--no-textconv", "--no-relative", "--find-renames"}

const (
	gitlinkMode    = "160000"
	symlinkMode    = "120000"
	modeRegular    = "100644"
	modeExecutable = "100755"

	gitDiff       = "diff"
	numstatFields = 3
)

// rootPathspec makes a pathspec name exactly one path from the repository
// root, whatever the working directory and whatever glob characters the path
// holds.
func rootPathspec(p string) string { return ":(top,literal)" + p }

type gitRunner struct {
	dir  string
	env  []string
	args []string
}

func (g gitRunner) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append(append([]string{}, g.args...), args...)...)
	cmd.Dir = g.dir
	cmd.Env = g.env
	return cmd
}

func (g gitRunner) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := g.command(ctx, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", args[0], msg)
	}
	return out, nil
}

func (g gitRunner) revParse(ctx context.Context, rev string) (string, error) {
	out, err := g.run(ctx, "rev-parse", "--verify", "--end-of-options", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", rev, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (g gitRunner) mergeBase(ctx context.Context, a, b string) (string, error) {
	out, err := g.run(ctx, "merge-base", a, b)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// defaultBranch returns the remote's default branch, falling back to a local
// main or master.
func (g gitRunner) defaultBranch(ctx context.Context) (string, error) {
	if out, err := g.run(ctx, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	for _, name := range []string{"main", "master"} {
		if _, err := g.revParse(ctx, name); err == nil {
			return name, nil
		}
	}
	return "", errors.New("no base given and no default branch found")
}

type rawChange struct {
	status  byte
	score   int
	oldMode string
	newMode string
	oldOID  string
	newOID  string
	oldPath string
	path    string
}

func (g gitRunner) rawChanges(ctx context.Context, base, head string) ([]rawChange, error) {
	args := append([]string{gitDiff, "--raw", "-z", "--no-abbrev"}, diffFlags...)
	out, err := g.run(ctx, append(args, base, head, "--")...)
	if err != nil {
		return nil, err
	}
	return parseRaw(out)
}

// parseRaw reads `git diff --raw -z` output.
func parseRaw(out []byte) ([]rawChange, error) {
	fields := strings.Split(string(out), "\x00")
	var changes []rawChange
	for i := 0; i < len(fields); i++ {
		meta := fields[i]
		if meta == "" {
			continue
		}
		parts := strings.Fields(strings.TrimPrefix(meta, ":"))
		if !strings.HasPrefix(meta, ":") || len(parts) != 5 || parts[4] == "" {
			return nil, fmt.Errorf("unexpected diff --raw record %q", meta)
		}
		c := rawChange{oldMode: parts[0], newMode: parts[1], oldOID: parts[2], newOID: parts[3], status: parts[4][0]}
		if len(parts[4]) > 1 {
			c.score, _ = strconv.Atoi(parts[4][1:])
		}
		if c.status == 'R' || c.status == 'C' {
			i++
			if i >= len(fields) {
				return nil, fmt.Errorf("truncated diff --raw record %q", meta)
			}
			c.oldPath = fields[i]
		}
		i++
		if i >= len(fields) || fields[i] == "" {
			return nil, fmt.Errorf("truncated diff --raw record %q", meta)
		}
		c.path = fields[i]
		changes = append(changes, c)
	}
	return changes, nil
}

type lineCounts struct {
	additions int
	deletions int
	binary    bool
}

func (g gitRunner) numstat(ctx context.Context, base, head string) ([]lineCounts, error) {
	args := append([]string{gitDiff, "--numstat", "-z"}, diffFlags...)
	out, err := g.run(ctx, append(args, base, head, "--")...)
	if err != nil {
		return nil, err
	}
	return parseNumstat(out)
}

// parseNumstat reads `git diff --numstat -z` output. Renames leave the path
// field empty and follow it with the old and new paths.
func parseNumstat(out []byte) ([]lineCounts, error) {
	fields := strings.Split(string(out), "\x00")
	var counts []lineCounts
	for i := 0; i < len(fields); i++ {
		if fields[i] == "" {
			continue
		}
		parts := strings.SplitN(fields[i], "\t", numstatFields)
		if len(parts) != numstatFields {
			return nil, fmt.Errorf("unexpected diff --numstat record %q", fields[i])
		}
		var c lineCounts
		if parts[0] == "-" {
			c.binary = true
		} else {
			c.additions, _ = strconv.Atoi(parts[0])
			c.deletions, _ = strconv.Atoi(parts[1])
		}
		if parts[2] == "" {
			i += 2
		}
		counts = append(counts, c)
	}
	return counts, nil
}

// builtinDrivers maps file patterns to git's built-in diff drivers, whose
// funcname patterns give better hunk headers than the default heuristic.
// A repository's own .gitattributes still takes precedence.
var builtinDrivers = []string{
	"*.go diff=golang",
	"*.py diff=python",
	"*.rb diff=ruby",
	"*.rs diff=rust",
	"*.java diff=java",
	"*.kt diff=kotlin",
	"*.cs diff=csharp",
	"*.php diff=php",
	"*.ex diff=elixir",
	"*.exs diff=elixir",
	"*.[ch] diff=cpp",
	"*.cc diff=cpp",
	"*.cpp diff=cpp",
	"*.hpp diff=cpp",
	"*.m diff=objc",
	"*.css diff=css",
	"*.html diff=html",
	"*.sh diff=bash",
	"*.pl diff=perl",
	"*.md diff=markdown",
	"*.markdown diff=markdown",
	"*.tex diff=tex",
}

// hunks returns each changed path's hunks, without context lines. Sections
// are matched to paths rather than counted, because git writes a type change,
// such as a file becoming a symlink, as two sections: a deletion and an
// addition of the same path.
func (g gitRunner) hunks(ctx context.Context, base, head string) (map[string][]review.Hunk, error) {
	args := append([]string{gitDiff, "--unified=0", "--src-prefix=a/", "--dst-prefix=b/"}, diffFlags...)
	cmd := g.command(ctx, append(args, base, head, "--")...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	byPath := map[string][]review.Hunk{}
	_, parseErr := review.ReadPatch(stdout, func(d review.FileDiff) error {
		byPath[d.Path] = append(byPath[d.Path], d.Hunks...)
		return nil
	})
	_, _ = io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("git diff: %s", strings.TrimSpace(stderr.String()))
	}
	return byPath, parseErr
}

// blobReader streams objects from a single `git cat-file --batch` process.
type blobReader struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
}

func (g gitRunner) blobs(ctx context.Context) (*blobReader, error) {
	cmd := g.command(ctx, "cat-file", "--batch")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &blobReader{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}, nil
}

// read returns up to limit bytes of the blob and its full size.
func (b *blobReader) read(oid string, limit int64) ([]byte, int64, error) {
	size, err := b.request(oid)
	if err != nil {
		return nil, 0, err
	}
	data := make([]byte, min(size, limit))
	if _, err := io.ReadFull(b.stdout, data); err != nil {
		return nil, 0, err
	}
	if _, err := b.stdout.Discard(int(size - int64(len(data)) + 1)); err != nil {
		return nil, 0, err
	}
	return data, size, nil
}

// copy writes the whole blob to w without holding it in memory.
func (b *blobReader) copy(oid string, w io.Writer) error {
	size, err := b.request(oid)
	if err != nil {
		return err
	}
	if _, err := io.CopyN(w, b.stdout, size); err != nil {
		return err
	}
	_, err = b.stdout.Discard(1)
	return err
}

// request asks for a blob and returns its size from the response header,
// "<oid> blob <size>" or "<oid> missing".
func (b *blobReader) request(oid string) (int64, error) {
	if _, err := fmt.Fprintln(b.stdin, oid); err != nil {
		return 0, err
	}
	header, err := b.stdout.ReadString('\n')
	if err != nil {
		return 0, err
	}
	if !strings.HasPrefix(header, oid+" blob ") {
		return 0, fmt.Errorf("cat-file %s: %s", oid, strings.TrimSpace(header))
	}
	return strconv.ParseInt(strings.TrimSpace(header[len(oid+" blob "):]), 10, 64)
}

func (b *blobReader) close() error {
	_ = b.stdin.Close()
	return b.cmd.Wait()
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
