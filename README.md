# review

review is a CLI and Go library that does the first pass of a code review without a model. It groups the net diff between two revisions and describes each group's additions, removals and changes in plain sentences. The result depends on the two endpoint trees, regardless of how the changes were split into commits. Mechanical edits are collapsed, and files that are safe to skim are listed separately. People and coding agents read the same output; an agent can run `review overview`, then `review show <n>` for one group's diff at a time.

[brief](https://github.com/git-pkgs/brief) reports the project's toolchain (languages, package managers, test runners, linters) and `brief diff` narrows that to what a branch touches, so run it alongside review, which describes the change itself:

```sh
brief diff main
review overview main
```

## Installation

Comparing revisions requires Git 2.41 or later, which supports reading attributes from a selected revision.

Install the CLI with `go install`, or add the library with `go get`:

```sh
go install github.com/git-pkgs/review/cmd/review@latest
go get github.com/git-pkgs/review
```

## Usage

```
$ review overview main
Branch topic
main...HEAD (2c6fe577e4c3..211b6441cd63), 3 files, +13 -2

Read in this order

1. Changes to lib/parse.go and tests  (2 files, +12 -1)
   Adds lib/parse_test.go. Changes the body of function `Parse`. Includes 1 test file.
     M lib/parse.go       +3 -1
     A lib/parse_test.go  +9 -0


Also changed

2. Documentation  (1 file, +1 -1)
   Changes docs/usage.md (# Usage).
```

Diffs use the selected head revision's `.gitattributes`, including when comparing older commits. Local attribute overrides do not affect line counts, hunk analysis or `show` output.

`review show <n>` prints the diff for one group:

```sh
review show 1 main
```

Files are grouped by links between their changed code and by test names. A changed call site and the changed declaration it uses go in one group, and each test goes with the file it is named after. Remaining files are grouped by directory. Groups are named after their files, with larger changes first; parts of a split group that other parts depend on are listed before their callers. Commit messages and intermediate edits do not affect grouping.

Tests are matched to the file they're named after across directories: `foo_test.go`, `foo.test.ts`, `test_foo.py`, `FooTest.java` and `foo_spec.rb` all map to `foo`. When several files are called `foo`, the test is paired with the one whose directory path best matches its own, so `spec/models/user_spec.rb` is paired with `app/models/user.rb` over `app/serializers/user.rb`. On a tie, the test is grouped by its own directory.

A group of more than 12 files is split into parts by directory, numbered like `3.1` and `3.2` (which `review show 3.2` accepts), at the first level where at least two directories hold three or more of its files. Each test is placed in the same part as the file it tests; remaining files are listed as "Other files".

Group descriptions are built from an [outline](https://github.com/git-pkgs/outline) graph of the changed files at the head commit, which maps each hunk to the declarations it touches, and from a comparison of outlines of both versions of each file. [roles](https://github.com/git-pkgs/roles) classifies what each file is for and [manifests](https://github.com/git-pkgs/manifests) identifies dependency files. Documentation, CI config, build files and dependency manifests are listed under "Also changed". Pure renames, whitespace-only edits, an identical edit repeated across files, and a repeated edit with different string or number values in three or more files (such as translations of new keys) are listed under "Mechanical changes". Whitespace-only means the same words in the same order; in Python, YAML, Makefiles and other files where indentation is syntax, the edit is limited to trailing spaces and blank lines. Generated files, vendored files, binaries and lockfiles are listed under "Safe to skim".

Ranges use git's syntax:

```
review overview              default branch...HEAD
review overview main         main...HEAD
review overview main...topic topic against its merge base with main
review overview main..topic  topic against main directly
review overview -C ../repo   run in another repository
review overview -v           list declaration changes under each file
review overview --format markdown  for pasting into a pull request
review overview --json       everything, including roles evidence and links between files
```

To review a standalone net diff, pass it with `--patch`, as a file or as `-` for stdin. It accepts `git diff` and `diff -u` output, downloaded GitHub `.diff` files, and a single `git format-patch` message. Multi-commit patch series are rejected; generate the end diff with `git diff base...HEAD` instead. A patch contains only the changed lines, so in patch mode modified files are described by their hunk context and their roles are inferred from the path, while added and deleted files are described by their declarations:

```sh
gh pr diff 123 | review overview --patch -
gh pr diff 123 | review show --patch - 2
git diff main...HEAD | review overview --patch -
```

## Library

The `review` package takes diffs and file contents as values, so it runs in WebAssembly or inside a service. Pass each file through `Analyze` and add the result to an `Overview`, then call `Group`:

```go
o := &review.Overview{Links: links}
for _, d := range diffs {
	f, err := review.Analyze(d)
	if err != nil {
		return err
	}
	o.Add(f)
}
o.Group()
o.Render(os.Stdout, review.TextOptions{Markdown: true})
```

A `FileDiff` contains the path, status, counts and hunks. Its `Base` and `Head` contents, `Symbols` and `BaseSymbols`, and the overview's `Links` between files are optional. With contents, declarations are compared between versions; with symbols, each hunk is labelled with the declarations it changed; with links, files are grouped and ordered by their dependencies. `ReadPatch` parses one unified diff into file sections and returns any single-message format-patch metadata. `FromPatch` runs the full parse, analyse and group sequence. `WritePatchFiles` writes the original sections for selected paths. Commit metadata does not affect grouping or rendered output.

The `gitdiff` package asks Git for the net diff between two revisions and fills in contents, symbols and links. It builds the head graph from the changed files, plus the `go.mod` and `__init__.py` files above them, written from the stored blobs. A second graph supplies base declarations for comparison. Unchanged source files are left out to limit parsing work:

```go
opts, _ := gitdiff.ParseRange([]string{"main"})
o, err := gitdiff.Build(ctx, ".", opts)
```

## Limitations

In a repository, review compares commits: staged and unstaged edits in the working tree are outside any range it reads, so commit them, or pass `git diff` output with `--patch`, to review them.

Whitespace inside string literals counts as whitespace, so `"a  b"` becoming `"a b"` is reported as a whitespace-only edit.

outline resolves imports between files for Go, Python and Ruby. JavaScript and TypeScript changes are grouped by test name and directory, and their summaries name declarations within each file.

## License

[MIT](LICENSE).
