// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package driverlint

import (
	"go/token"
	"go/types"
	"slices"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/ssa"
)

const csiPath = "github.com/container-storage-interface/spec/lib/go/csi"

// CSIStatus catches obvious unconverted backend errors at CSI RPC boundaries.
var CSIStatus = &analysis.Analyzer{
	Name:     "csistatus",
	Doc:      "translate backend errors to gRPC statuses at CSI RPC boundaries",
	Requires: []*analysis.Analyzer{buildssa.Analyzer},
	Run:      runCSIStatus,
}

func runCSIStatus(pass *analysis.Pass) (any, error) {
	switch pass.Pkg.Path() {
	case "local-csi-driver/internal/csi/controller", "local-csi-driver/internal/csi/node",
		"local-csi-driver/internal/csi/identity", "local-csi-driver/internal/csi/server":
		for _, fn := range pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA).SrcFuncs {
			if isRPC(fn.Signature, fn.Name()) {
				checkRPCReturns(pass, fn)
			}
		}
	}
	return nil, nil
}

func isRPC(sig *types.Signature, name string) bool {
	if sig.Recv() == nil || sig.Params().Len() != 2 || sig.Results().Len() != 2 {
		return false
	}
	return csiMessage(sig.Params().At(1).Type(), name+"Request") &&
		csiMessage(sig.Results().At(0).Type(), name+"Response") &&
		types.Identical(sig.Results().At(1).Type(), types.Universe.Lookup("error").Type())
}

func csiMessage(typ types.Type, name string) bool {
	ptr, ok := types.Unalias(typ).(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := types.Unalias(ptr.Elem()).(*types.Named)
	return ok && inPackage(named.Obj(), csiPath) && named.Obj().Name() == name
}

func checkRPCReturns(pass *analysis.Pass, fn *ssa.Function) {
	for _, block := range fn.Blocks {
		for _, instruction := range block.Instrs {
			if ret, ok := instruction.(*ssa.Return); ok && unconverted(ret.Results[1], make(map[ssa.Value]bool)) {
				pass.Reportf(ret.Pos(), "translate backend error to a gRPC status before returning from CSI RPC %s", fn.Name())
			}
		}
	}
}

func unconverted(value ssa.Value, visited map[ssa.Value]bool) bool {
	if visited[value] {
		return false
	}
	visited[value] = true
	switch value := value.(type) {
	case *ssa.Call:
		return plainError(value.Common())
	case *ssa.Extract:
		return backendResult(value.Tuple)
	case *ssa.Phi:
		return anyUnconverted(value.Edges, visited)
	case *ssa.UnOp:
		return unconverted(localResultStore(value), visited)
	default:
		return false
	}
}

// With defer, SSA can spill an otherwise direct return value to a local slot.
func localResultStore(load *ssa.UnOp) ssa.Value {
	if load.Op != token.MUL {
		return nil
	}
	instructions := load.Block().Instrs
	for i := slices.Index(instructions, ssa.Instruction(load)) - 1; i >= 0; i-- {
		if store, ok := instructions[i].(*ssa.Store); ok && store.Addr == load.X {
			return store.Val
		}
	}
	return nil
}

func anyUnconverted(edges []ssa.Value, visited map[ssa.Value]bool) bool {
	for _, edge := range edges {
		if unconverted(edge, visited) {
			return true
		}
	}
	return false
}

func plainError(call *ssa.CallCommon) bool {
	callee := call.StaticCallee()
	if callee == nil {
		return false
	}
	return callee.RelString(nil) == "fmt.Errorf" || strings.HasPrefix(callee.RelString(nil), "errors.")
}

func backendResult(tuple ssa.Value) bool {
	call, ok := tuple.(*ssa.Call)
	if !ok {
		return false
	}
	// A forwarded CSI response is allowed to carry the upstream status.
	results := call.Common().Signature().Results()
	ptr, ok := types.Unalias(results.At(0).Type()).(*types.Pointer)
	if !ok {
		return true
	}
	named, ok := types.Unalias(ptr.Elem()).(*types.Named)
	return !ok || !inPackage(named.Obj(), csiPath)
}
