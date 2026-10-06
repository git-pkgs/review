package gitdiff

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// diffRepository shares objects with the source repository but reads attributes
// from head. A separate git directory excludes local info/attributes and config.
func (g gitRunner) diffRepository(ctx context.Context, head string) (gitRunner, func(), error) {
	out, err := g.run(ctx, "rev-parse", "--path-format=absolute", "--git-path", "objects", "--show-object-format")
	if err != nil {
		return gitRunner{}, nil, err
	}
	objects, format, ok := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if !ok {
		return gitRunner{}, nil, fmt.Errorf("unexpected git object directory and format: %q", out)
	}
	dir, err := os.MkdirTemp("", "review-diff-*")
	if err != nil {
		return gitRunner{}, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	diff := gitRunner{dir: dir, env: diffEnvironment()}
	if _, err := diff.run(ctx, "init", "--bare", "--quiet", "--template=", "--object-format="+format); err != nil {
		cleanup()
		return gitRunner{}, nil, err
	}
	diff.env = append(diff.env, "GIT_OBJECT_DIRECTORY="+objects)
	attrs := filepath.Join(dir, "attributes")
	const filePerm = 0o600
	if err := os.WriteFile(attrs, []byte(strings.Join(builtinDrivers, "\n")+"\n"), filePerm); err != nil {
		cleanup()
		return gitRunner{}, nil, err
	}
	diff.args = []string{"--attr-source=" + head, "-c", "core.attributesFile=" + attrs}
	return diff, cleanup, nil
}

func diffEnvironment() []string {
	var env []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			env = append(env, entry)
		}
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_ATTR_NOSYSTEM=1")
}
