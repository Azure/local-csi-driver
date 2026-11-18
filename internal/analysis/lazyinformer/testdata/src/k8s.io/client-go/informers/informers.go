// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package informers

type Factory interface {
	InformerFor()
	ForResource() GenericInformer
	Start()
}

type GenericInformer interface {
	Lister()
}

func NewSharedInformerFactory() Factory { return nil }
