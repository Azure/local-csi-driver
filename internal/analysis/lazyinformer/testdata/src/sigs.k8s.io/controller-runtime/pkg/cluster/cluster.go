// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cluster

import (
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Cluster interface {
	GetAPIReader() client.Reader
	GetClient() client.Client
	GetCache() cache.Cache
	GetFieldIndexer() client.FieldIndexer
}
