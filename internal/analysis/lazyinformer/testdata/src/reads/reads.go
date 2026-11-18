// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package reads

import (
	"github.com/open-policy-agent/cert-controller/pkg/rotator"
	"k8s.io/client-go/informers"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
)

type manager interface {
	cluster.Cluster
	Start()
}

func read(mgr manager, injected client.Client, useCache bool) {
	mgr.GetClient().Get() // want "client.Get may create an informer"
	mgr.GetClient().Update()
	mgr.GetCache().GetInformer() // want "cache.GetInformer may create an informer"
	mgr.GetCache().List()        // want "client.List may create an informer"

	var reader client.Reader = mgr.GetClient()
	reader.List() // want "client.List may create an informer"

	reader = mgr.GetAPIReader()
	reader.Get()
	if useCache {
		reader = mgr.GetCache()
	}
	reader.Get() // want "client.Get may create an informer"

	mgr.GetAPIReader().List()
	client.New().Get()
	injected.Get()
}

func explicit(mgr manager, watches *builder.Builder[int], injected cache.Cache) {
	mgr.GetCache().GetInformerForKind() // want "GetInformerForKind may create an informer"
	mgr.GetFieldIndexer().IndexField()  // want "IndexField may create an informer"
	watches.For()                       // want "For may create an informer"
	watches.WatchesRawSource()
	injected.List() // want "client.List may create an informer"
	factory := informers.NewSharedInformerFactory()
	factory.InformerFor()            // want "InformerFor may create an informer"
	generic := factory.ForResource() // want "ForResource may create an informer"
	factory.Start()
	generic.Lister()
	rotator.AddRotator() // want "rotator.AddRotator may create an informer"
}

func suppression(mgr manager) {
	//lazyinformer:ignore Maintain the PV index for reconciliation.
	mgr.GetFieldIndexer().IndexField()
	mgr.GetCache().GetInformer() //lazyinformer:ignore Explicit startup watch for PV cleanup.
	//lazyinformer:ignore
	mgr.GetClient().Get() // want "Get may create an informer"
	//lazyinformer:ignore Scope applies to the following line only.
	mgr.GetAPIReader().Get()
	mgr.GetClient().List() // want "List may create an informer"
}

func loop(mgr manager, n int) {
	reader := mgr.GetAPIReader()
	for i := 0; i < n; i++ {
		reader.List() // want "client.List may create an informer"
		reader = mgr.GetClient()
	}
}

type unrelated interface {
	GetClient() client.Client
}

func ignoreSameMethodName(other unrelated) {
	other.GetClient().List()
}
