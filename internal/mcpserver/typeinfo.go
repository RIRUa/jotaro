package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
)

// listExports runs `go list -export -deps -json` (args) in dir and returns the export data file of each package,
// by import path. The packages were just built, so this reads the build cache.
func listExports(ctx context.Context, dir string, args []string) (map[string]string, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list: %s", firstLines(stderr.String(), 5))
	}
	exports := map[string]string{}
	dec := json.NewDecoder(&stdout)
	for {
		var p struct{ ImportPath, Export string }
		if err := dec.Decode(&p); err != nil {
			if errors.Is(err, io.EOF) {
				return exports, nil
			}
			return nil, fmt.Errorf("go list: %w", err)
		}
		if p.Export != "" {
			exports[p.ImportPath] = p.Export
		}
	}
}

// typeInfo type-checks f against the compiled export data of its dependencies and returns the result, which the
// lint rules use to match objects instead of names. exportErr is why exports is missing, if it is.
//
// The importer panics on export data written by a newer Go than the one this binary was built with, so a panic is
// turned into an error: the caller then falls back to the syntactic lint instead of taking the server down.
func typeInfo(fset *token.FileSet, f *ast.File, exports map[string]string, exportErr error) (info *types.Info, err error) {
	defer func() {
		if r := recover(); r != nil {
			info, err = nil, fmt.Errorf("type information is unavailable: %v", r)
		}
	}()
	if exportErr != nil {
		return nil, exportErr
	}
	lookup := func(path string) (io.ReadCloser, error) {
		file, ok := exports[path]
		if !ok {
			return nil, fmt.Errorf("no export data for %q", path)
		}
		return os.Open(file)
	}
	info = &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	var first error
	conf := types.Config{
		Importer: importer.ForCompiler(fset, "gc", lookup),
		Error: func(err error) {
			if first == nil {
				first = err
			}
		},
	}
	// The code compiled, so an error here means the export data could not be read.
	if _, err = conf.Check("main", fset, []*ast.File{f}, info); err != nil && first != nil {
		return nil, fmt.Errorf("type information is unavailable: %w", first)
	}
	return info, nil
}
