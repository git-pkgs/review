# review

review is a CLI and Go library that does the first pass of a code review without a model. It splits a change into groups in the order they're worth reading and describes each group's additions, removals and changes in plain sentences. Mechanical edits are collapsed, and files that are safe to skim are listed separately. People and coding agents read the same output; an agent can run `review overview`, then `review show <n>` for one group's diff at a time.

[brief](https://github.com/git-pkgs/brief) reports the project's toolchain (languages, package managers, test runners, linters) and `brief diff` narrows that to what a branch touches, so run it alongside review, which describes the change itself:

```sh
brief diff main
review overview main
```

## Installation

Install the CLI with `go install`, or add the library with `go get`:

```sh
go install github.com/git-pkgs/review/cmd/review@latest
go get github.com/git-pkgs/review
```

## Usage

```
$ review overview 699893a^..699893a
Add vulnerability scan failure thresholds (#373)
699893a^..699893a (4bf393f32bd3..699893a77376), 1 commit, 4 files, +397 -23

Read in this order

1. Add vulnerability scan failure thresholds (#373)  (2 files, +377 -17)
   Adds cmd/vulns_scan_test.go. Adds variable `ErrVulnerabilityPolicy`, and function
   `vulnerabilityPolicyError`. Changes the declaration of function `outputVulnsScan`.
   Changes the body of functions `syncVulnerabilitiesForDeps`, `addVulnsScanCmd`,
   `runVulnsScan` and `scanLiveWithSource`. Includes 1 test file. Documented in
   docs/ci-cd.md and docs/vulns.md.
     M cmd/vulns.go            +76 -17
     A cmd/vulns_scan_test.go  +301 -0


Also changed

2. Documentation  (2 files, +20 -6)
   Changes docs/ci-cd.md (### Vulnerability scanning with SARIF, ### Block PRs with high
   severity vulnerabilities, ### Vulnerability scanning); docs/vulns.md (## Examples).
```

`review show <n>` prints the diff for one group:

```sh
review show 1 699893a^..699893a
```

When a range has several commits, each file is assigned to the commit that changed it most, each commit becomes a group named after its subject, and small fixup commits are merged into the commit they fix. With zero or one commit, files are grouped by how their changed code is linked, and groups whose declarations other groups use are listed first. A changed call site and the changed declaration it uses go in one group, and each test goes with the file it is named after. Remaining files are grouped by directory.

Tests are matched to the file they're named after across directories: `foo_test.go`, `foo.test.ts`, `test_foo.py`, `FooTest.java` and `foo_spec.rb` all map to `foo`. When several files are called `foo`, the test is paired with the one whose directory path best matches its own, so `spec/models/user_spec.rb` is paired with `app/models/user.rb` over `app/serializers/user.rb`. On a tie, the test is grouped by its own directory.

A group of more than 12 files is split into parts by directory, numbered like `3.1` and `3.2` (which `review show 3.2` accepts), at the first level where at least two directories hold three or more of its files. Each test is placed in the same part as the file it tests; remaining files are listed as "Other files".

Group descriptions are built from an [outline](https://github.com/git-pkgs/outline) graph of the head tree, which maps each hunk to the declarations it touches, and from a comparison of outlines of both versions of each file. [roles](https://github.com/git-pkgs/roles) classifies what each file is for and [manifests](https://github.com/git-pkgs/manifests) identifies dependency files. Documentation, CI config, build files and dependency manifests are listed under "Also changed". Pure renames, whitespace-only edits, an identical edit repeated across files, and a repeated edit with different string or number values in three or more files (such as translations of new keys) are listed under "Mechanical changes". Whitespace-only means the same words in the same order; in Python, YAML, Makefiles and other files where indentation is syntax, the edit is limited to trailing spaces and blank lines. Generated files, vendored files, binaries and lockfiles are listed under "Safe to skim".

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

To review a standalone diff, pass it with `--patch`, as a file or as `-` for stdin. It accepts `git diff` and `diff -u` output, `git format-patch` output including commit messages, and GitHub's `.diff` URLs. A patch contains only the changed lines, so in patch mode modified files are described by their hunk context and their roles are inferred from the path, while added and deleted files are described by their declarations:

```sh
gh pr diff 123 | review overview --patch -
gh pr diff 123 | review show --patch - 2
```

## Library

The `review` package takes diffs and file contents as values, so it runs in WebAssembly or inside a service. Pass each file through `Analyze` and add the result to an `Overview`, then call `Group`:

```go
o := &review.Overview{Commits: commits, Links: links}
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

A `FileDiff` contains the path, status, counts and hunks. Its `Base` and `Head` contents, its head `Symbols` and the overview's `Links` between files are optional. With contents, declarations are compared between versions; with symbols, each hunk is labelled with the declarations it changed; with links, files are grouped and ordered by their dependencies. `ReadPatch` parses a unified diff into `FileDiff` values and commits, and `FromPatch` runs the full parse, analyse and group sequence on a patch.

The `gitdiff` package reads a range from a local repository with the git binary and fills in contents, symbols and links. It builds the outline graph from a temporary copy of the head commit's files, written from the stored blobs, so the result depends only on the commits in the range:

```go
opts, _ := gitdiff.ParseRange([]string{"main"})
o, err := gitdiff.Build(ctx, ".", opts)
```

## Limitations

In a repository, review compares commits: staged and unstaged edits in the working tree are outside any range it reads, so commit them, or pass `git diff` output with `--patch`, to review them.

Whitespace inside string literals counts as whitespace, so `"a  b"` becoming `"a b"` is reported as a whitespace-only edit.

outline resolves imports between files for Go, Python and Ruby. JavaScript and TypeScript changes are grouped by commit, test name and directory, and their summaries name declarations within each file.

## License

[MIT](LICENSE).
