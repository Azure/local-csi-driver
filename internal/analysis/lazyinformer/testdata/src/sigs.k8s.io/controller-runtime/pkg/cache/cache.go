// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cache

import "sigs.k8s.io/controller-runtime/pkg/client"

type Cache interface {
	client.Reader
	GetInformer()
	GetInformerForKind()
}
