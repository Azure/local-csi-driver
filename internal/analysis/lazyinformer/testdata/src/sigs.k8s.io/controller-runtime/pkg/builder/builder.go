// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package builder

type Builder[T any] struct{}

func (*Builder[T]) For()              {}
func (*Builder[T]) WatchesRawSource() {}
