// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package suppression

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"golang.org/x/tools/go/analysis"
)

func TestHasReason(t *testing.T) {
	const source = `package p
func f() {
	f() //lazyinformer:ignore watches Pods
	//lazyinformer:ignore watches Nodes
	f()

	f()
	f() //lazyinformer:ignore
	f() //lvmsize:ignore watches Pods
	f()
	//lazyinformer:ignore watches Secrets
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "calls.go", source, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	pass := &analysis.Pass{Fset: fset, Files: []*ast.File{file}}
	for _, line := range []int{3, 5, 7, 8, 9, 10} {
		pos := fset.File(file.Pos()).LineStart(line) + 1
		want := line == 3 || line == 5
		if got := HasReason(pass, pos, "lazyinformer"); got != want {
			t.Errorf("HasReason at line %d = %t, want %t", line, got, want)
		}
	}
	other := fset.AddFile("generated.go", -1, 10)
	if HasReason(pass, other.Pos(1), "lazyinformer") {
		t.Error("suppression must not cross source files")
	}
}
