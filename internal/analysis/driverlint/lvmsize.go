// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package driverlint checks local CSI driver conventions.
package driverlint

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"regexp"
	"slices"

	"golang.org/x/tools/go/analysis"

	"local-csi-driver/internal/analysis/suppression"
)

// LVMSize catches size values that LVM can interpret in implicit MiB.
var LVMSize = &analysis.Analyzer{
	Name: "lvmsize",
	Doc:  "require explicit units for LVM create, extend and reduce size inputs",
	Run:  runLVMSize,
}

var bareSize = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)$`)

func runLVMSize(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.CompositeLit:
				checkSizeLiteral(pass, node)
			case *ast.AssignStmt:
				checkSizeAssignment(pass, node)
			}
			return true
		})
	}
	return nil, nil
}

func checkSizeLiteral(pass *analysis.Pass, literal *ast.CompositeLit) {
	if !lvmOptions(pass.TypesInfo.TypeOf(literal)) {
		return
	}
	for _, element := range literal.Elts {
		if field, ok := element.(*ast.KeyValueExpr); ok && isSizeField(field.Key) {
			checkSizeValue(pass, field.Value, field.Pos())
		}
	}
}

func checkSizeAssignment(pass *analysis.Pass, assign *ast.AssignStmt) {
	if len(assign.Lhs) != len(assign.Rhs) {
		return
	}
	for i, lhs := range assign.Lhs {
		sel, ok := lhs.(*ast.SelectorExpr)
		if ok && isSizeField(sel.Sel) && lvmOptions(pass.TypesInfo.TypeOf(sel.X)) {
			checkSizeValue(pass, assign.Rhs[i], assign.Pos())
		}
	}
}

func isSizeField(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == "Size"
}

func lvmOptions(typ types.Type) bool {
	if ptr, ok := types.Unalias(typ).(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	named, ok := types.Unalias(typ).(*types.Named)
	return ok && inPackage(named.Obj(), "local-csi-driver/internal/pkg/lvm") &&
		slices.Contains([]string{"CreateLVOptions", "ExtendLVOptions", "ReduceLVOptions"}, named.Obj().Name())
}

func checkSizeValue(pass *analysis.Pass, value ast.Expr, pos token.Pos) {
	if ambiguousSize(pass.TypesInfo, ast.Unparen(value)) && !suppression.HasReason(pass, pos, "lvmsize") {
		pass.Reportf(value.Pos(), "LVM size may be unitless; format byte counts with an explicit B suffix")
	}
}

func ambiguousSize(info *types.Info, expr ast.Expr) bool {
	if value := info.Types[expr].Value; value != nil && value.Kind() == constant.String {
		return bareSize.MatchString(constant.StringVal(value))
	}
	call, ok := expr.(*ast.CallExpr)
	return ok && ambiguousSizeCall(info, call)
}

func ambiguousSizeCall(info *types.Info, call *ast.CallExpr) bool {
	fn := calledObject(info, call)
	switch {
	case inPackage(fn, "k8s.io/apimachinery/pkg/api/resource"):
		return fn.Name() == "String"
	case inPackage(fn, "strconv"):
		return slices.Contains([]string{"Itoa", "FormatInt", "FormatUint"}, fn.Name())
	case inPackage(fn, "fmt") && fn.Name() == "Sprintf":
		format := info.Types[call.Args[0]].Value
		return format != nil && slices.Contains([]string{"%d", "+%d", "%+d", "-%d"}, constant.StringVal(format))
	default:
		return false
	}
}

func calledObject(info *types.Info, call *ast.CallExpr) types.Object {
	switch fn := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		return info.Uses[fn]
	case *ast.SelectorExpr:
		return info.Uses[fn.Sel]
	default:
		return nil
	}
}

func inPackage(obj types.Object, path string) bool {
	return obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == path
}
