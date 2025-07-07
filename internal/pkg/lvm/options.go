// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

/* SPDX-License-Identifier: Apache-2.0
 *
 * Copyright 2023 Damian Peckett <damian@pecke.tt>.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package lvm

import (
	"local-csi-driver/internal/pkg/block"

	"go.opentelemetry.io/otel/trace"
)

// ClientOption is an option for configuring the lvm2 client.
type ClientOption func(*Client)

// Set the path to the lvm executable.
func WithLVM(path string) ClientOption {
	return func(c *Client) {
		c.lvmPath = path
	}
}

// Set the path to the system utilities executable.
func WithSystemUtilities(utils block.Interface) ClientOption {
	return func(c *Client) {
		c.sysutils = utils
	}
}

// Set the tracer.
func WithTracerProvider(tp trace.TracerProvider) ClientOption {
	return func(c *Client) {
		c.tracer = tp.Tracer("localdisk.csi.acstor.io/internal/pkg/lvm")
	}
}
