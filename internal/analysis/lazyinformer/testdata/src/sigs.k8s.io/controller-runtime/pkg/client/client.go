// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package client

type Reader interface {
	Get()
	List()
}

type Client interface {
	Reader
	Update()
}

type FieldIndexer interface {
	IndexField()
}

func New() Client { return nil }
