// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package suppression recognizes reasoned, line-scoped analyzer exceptions.
package suppression

import (
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// HasReason reports whether an ignore comment with a reason is on or immediately above pos.
func HasReason(pass *analysis.Pass, pos token.Pos, name string) bool {
	prefix := "//" + name + ":ignore "
	for _, file := range pass.Files {
		if file.Pos() <= pos && pos <= file.End() {
			for _, group := range file.Comments {
				for _, comment := range group.List {
					if reasonAt(pass.Fset, comment.Pos(), pos, comment.Text, prefix) {
						return true
					}
				}
			}
		}
	}
	return false
}

func reasonAt(fset *token.FileSet, comment, target token.Pos, text, prefix string) bool {
	line := fset.PositionFor(target, false).Line
	commentLine := fset.PositionFor(comment, false).Line
	reason, ok := strings.CutPrefix(text, prefix)
	return ok && strings.TrimSpace(reason) != "" && (commentLine == line || commentLine == line-1)
}
