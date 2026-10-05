package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"

	"github.com/git-pkgs/review"
	"github.com/git-pkgs/review/gitdiff"
	"github.com/spf13/cobra"
)

const rangeHelp = `A range follows git's syntax:
  (none)     default branch...HEAD
  base       base...HEAD
  a...b      b against the merge base of a and b
  a..b       b against a
  a b        same as a...b`

// exitInterrupted is the shell convention for a process ended by SIGINT.
const exitInterrupted = 130

// maxRevisions is the most revisions a range takes, as in "review overview a b".
const maxRevisions = 2

const (
	formatText     = "text"
	formatMarkdown = "markdown"
	formatJSON     = "json"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	switch {
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(os.Stderr, "interrupted")
		os.Exit(exitInterrupted)
	case err != nil:
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	root := newRootCmd()
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	return root.ExecuteContext(ctx)
}

type source struct {
	dir   string
	patch string
}

func newRootCmd() *cobra.Command {
	src := &source{}
	root := &cobra.Command{
		Use:           "review",
		Short:         "Break a change into groups in reading order before reviewing it",
		Version:       moduleVersion(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&src.dir, "dir", "C", ".", "run in this repository")
	root.PersistentFlags().StringVar(&src.patch, "patch", "", "read a unified diff from this file, or - for stdin, instead of running git")
	root.AddCommand(newOverviewCmd(src), newShowCmd(src))
	return root
}

func newOverviewCmd(src *source) *cobra.Command {
	var format string
	var asJSON, verbose bool
	cmd := &cobra.Command{
		Use:   "overview [range]",
		Short: "Group the change in reading order, with what can be skimmed",
		Long:  "Group the change in reading order, with what can be skimmed.\n\n" + rangeHelp,
		Args:  cobra.MaximumNArgs(maxRevisions),
		RunE: func(cmd *cobra.Command, args []string) error {
			if asJSON {
				format = formatJSON
			}
			if !slices.Contains([]string{formatText, formatMarkdown, formatJSON}, format) {
				return fmt.Errorf("unknown format %q", format)
			}
			o, _, err := src.overview(cmd.Context(), args, cmd.InOrStdin())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if format == formatJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(o)
			}
			return o.Render(out, review.TextOptions{Markdown: format == formatMarkdown, Declarations: verbose})
		},
	}
	cmd.Flags().StringVarP(&format, "format", "f", formatText, "output format: text, markdown or json")
	cmd.Flags().BoolVar(&asJSON, "json", false, "same as --format json")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "list declaration changes under each file")
	return cmd
}

func newShowCmd(src *source) *cobra.Command {
	return &cobra.Command{
		Use:   "show <group> [range]",
		Short: "Print the diff of one group from the overview",
		Long:  "Print the diff of one group from the overview. A part of a split group is\nnamed with a dot, as in 3.2.\n\n" + rangeHelp,
		Args:  cobra.RangeArgs(1, 1+maxRevisions),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseGroupID(args[0])
			if err != nil {
				return err
			}
			o, patch, err := src.overview(cmd.Context(), args[1:], cmd.InOrStdin())
			if err != nil {
				return err
			}
			group, err := findGroup(o.Groups, ids, args[0])
			if err != nil {
				return err
			}
			return showGroup(cmd.Context(), src, o, group, args[0], patch, cmd.OutOrStdout())
		},
	}
}

// parseGroupID reads "3" or "3.2" as a path of group numbers.
func parseGroupID(s string) ([]int, error) {
	var ids []int
	for part := range strings.SplitSeq(s, ".") {
		id, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("group %q is not a number like 3 or 3.2", s)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func findGroup(groups []review.Group, ids []int, label string) (review.Group, error) {
	var g review.Group
	for _, id := range ids {
		idx := slices.IndexFunc(groups, func(c review.Group) bool { return c.ID == id })
		if idx < 0 {
			return g, fmt.Errorf("no group %s", label)
		}
		g = groups[idx]
		groups = g.Children
	}
	return g, nil
}

func showGroup(ctx context.Context, src *source, o *review.Overview, group review.Group, label string, patch []byte, w io.Writer) error {
	if !strings.Contains(label, ".") {
		label += "."
	}
	if _, err := fmt.Fprintf(w, "# %s %s\n", label, group.Title); err != nil {
		return err
	}
	if patch == nil {
		paths := slices.Clone(group.Files)
		for _, f := range o.Files {
			if f.OldPath != "" && slices.Contains(group.Files, f.Path) {
				paths = append(paths, f.OldPath)
			}
		}
		return gitdiff.Diff(ctx, src.dir, *o.Range, paths, w)
	}
	_, err := review.ReadPatchSections(bytes.NewReader(patch), func(d review.FileDiff, section string) error {
		if !slices.Contains(group.Files, d.Path) {
			return nil
		}
		_, err := io.WriteString(w, section)
		return err
	})
	return err
}

// read returns the patch bytes when the source is a patch. Reading stdin
// stops when ctx is cancelled, so an interrupt ends a wait for input.
func (s *source) read(ctx context.Context, stdin io.Reader) ([]byte, error) {
	if s.patch != "-" {
		return os.ReadFile(s.patch)
	}
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(stdin)
		done <- result{data, err}
	}()
	select {
	case r := <-done:
		return r.data, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *source) overview(ctx context.Context, revs []string, stdin io.Reader) (*review.Overview, []byte, error) {
	if s.patch == "" {
		opts, err := gitdiff.ParseRange(revs)
		if err != nil {
			return nil, nil, err
		}
		o, err := gitdiff.Build(ctx, s.dir, opts)
		return o, nil, err
	}
	if len(revs) > 0 {
		return nil, nil, errors.New("--patch cannot be combined with revisions")
	}
	data, err := s.read(ctx, stdin)
	if err != nil {
		return nil, nil, err
	}
	o, err := review.FromPatch(bytes.NewReader(data))
	return o, data, err
}

// version is set by release builds with -ldflags "-X main.version=...";
// go install builds report the module version instead.
var version string

func moduleVersion() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" {
		return "(devel)"
	}
	return info.Main.Version
}
