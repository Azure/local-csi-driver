// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package lazyinformer finds reads that may implicitly start an informer.
package lazyinformer

import (
	"go/types"
	"slices"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/ssa"

	"local-csi-driver/internal/analysis/suppression"
)

const runtimePath = "sigs.k8s.io/controller-runtime/pkg/"

// Analyzer reports reads from a controller-runtime manager or cluster cache.
var Analyzer = &analysis.Analyzer{
	Name: "lazyinformer",
	Doc: `report controller-runtime calls that may create an informer

The first cache-backed read of a kind can start a LIST/WATCH. Use GetAPIReader
for intentional direct reads, or explicitly register the informer and consider
ReaderFailOnMissingInformer to enforce that policy at runtime.

Explicit cache lookups, field indexing, controller watches and client-go
informer acquisition can also create informers. Intentional calls require
//lazyinformer:ignore <reason> on or
immediately above the call.

Get and List are checked on typed caches and locally traced manager clients.
Provenance is not followed through fields, helper returns, or captures typed as
client.Client or client.Reader. This checker cannot determine whether a manager
bypasses the cache for a kind, rejects missing informers, or already watches it.`,
	Requires: []*analysis.Analyzer{buildssa.Analyzer},
	Run:      run,
}

func run(pass *analysis.Pass) (any, error) {
	functions := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA).SrcFuncs
	for _, fn := range functions {
		for _, block := range fn.Blocks {
			for _, instruction := range block.Instrs {
				if call, ok := instruction.(ssa.CallInstruction); ok {
					checkCall(pass, call.Common())
				}
			}
		}
	}
	return nil, nil
}

func checkCall(pass *analysis.Pass, call *ssa.CallCommon) {
	method := calledFunction(call)
	if !createsInformer(method, call.Value) {
		return
	}
	if suppression.HasReason(pass, call.Pos(), "lazyinformer") {
		return
	}
	pass.Reportf(call.Pos(), "%s.%s may create an informer that starts a LIST/WATCH; use a direct read when appropriate or add //lazyinformer:ignore <reason> for an intentional informer", method.Pkg().Name(), method.Name())
}

func calledFunction(call *ssa.CallCommon) *types.Func {
	if call.Method != nil {
		return call.Method
	}
	if fn := call.StaticCallee(); fn != nil {
		method, _ := fn.Object().(*types.Func)
		return method
	}
	return nil
}

func createsInformer(method *types.Func, receiver ssa.Value) bool {
	return runtimeInformer(method) || clientGoInformer(method) || cachedRead(method, receiver)
}

func runtimeInformer(method *types.Func) bool {
	switch {
	case runtimeMethod(method, "cache", "GetInformer", "GetInformerForKind"):
		return true
	case runtimeMethod(method, "client", "IndexField"):
		return true
	case runtimeMethod(method, "source", "Kind", "TypedKind"):
		return true
	case runtimeMethod(method, "builder", "For", "Owns", "Watches", "WatchesMetadata"):
		return true
	default:
		return namedCall(method, "github.com/open-policy-agent/cert-controller/pkg/rotator", "AddRotator")
	}
}

func cachedRead(method *types.Func, receiver ssa.Value) bool {
	return runtimeMethod(method, "client", "Get", "List") && fromCache(receiver, make(map[ssa.Value]bool))
}

func clientGoInformer(fn *types.Func) bool {
	if fn == nil || fn.Pkg() == nil {
		return false
	}
	path, ok := strings.CutPrefix(fn.Pkg().Path(), "k8s.io/client-go/")
	if !ok {
		return false
	}
	return clientGoAcquisition(path, fn.Name())
}

func clientGoAcquisition(path, name string) bool {
	switch path {
	case "tools/cache":
		return slices.Contains([]string{"NewInformer", "NewIndexerInformer", "NewInformerWithOptions",
			"NewTransformingInformer", "NewTransformingIndexerInformer",
			"NewSharedInformer", "NewSharedIndexInformer", "NewSharedIndexInformerWithOptions"}, name)
	case "dynamic/dynamicinformer", "metadata/metadatainformer":
		return slices.Contains([]string{"ForResource", "NewFilteredDynamicInformer", "NewFilteredMetadataInformer"}, name)
	default:
		return generatedInformer(path, name)
	}
}

func generatedInformer(path, name string) bool {
	if path == "informers" {
		return slices.Contains([]string{"ForResource", "InformerFor"}, name)
	}
	return strings.HasPrefix(path, "informers/") && (slices.Contains([]string{"Informer", "Lister", "InformerFor"}, name) || informerConstructor(name))
}

func informerConstructor(name string) bool {
	return strings.HasPrefix(name, "New") && strings.HasSuffix(name, "Informer")
}

func fromCache(value ssa.Value, visited map[ssa.Value]bool) bool {
	if visited[value] {
		return false
	}
	visited[value] = true
	if isRuntimeCache(value.Type()) {
		return true
	}
	switch value := value.(type) {
	case *ssa.Call:
		return runtimeMethod(value.Call.Method, "cluster", "GetClient", "GetCache")
	case *ssa.ChangeInterface:
		return fromCache(value.X, visited)
	case *ssa.Phi:
		return anyCacheEdge(value.Edges, visited)
	default:
		return false
	}
}

func isRuntimeCache(typ types.Type) bool {
	named, ok := types.Unalias(typ).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == runtimePath+"cache" && named.Obj().Name() == "Cache"
}

func anyCacheEdge(edges []ssa.Value, visited map[ssa.Value]bool) bool {
	for _, edge := range edges {
		if fromCache(edge, visited) {
			return true
		}
	}
	return false
}

func runtimeMethod(method *types.Func, pkg string, names ...string) bool {
	return namedCall(method, runtimePath+pkg, names...)
}

func namedCall(method *types.Func, path string, names ...string) bool {
	if method == nil || method.Pkg() == nil || method.Pkg().Path() != path {
		return false
	}
	return slices.Contains(names, method.Name())
}
