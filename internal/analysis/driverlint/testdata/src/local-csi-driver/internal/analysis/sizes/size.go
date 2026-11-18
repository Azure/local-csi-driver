// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sizes

import (
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/api/resource"

	"local-csi-driver/internal/pkg/lvm"
)

func sizes(q resource.Quantity, bytes int64) {
	_ = lvm.CreateLVOptions{Size: "16Mi"}
	_ = lvm.CreateLVOptions{Size: "-16.5"} // want "LVM size may be unitless"
	_ = lvm.CreateLVOptions{Size: fmt.Sprintf("%dB", bytes)}
	_ = lvm.ExtendLVOptions{Size: q.String()}                   // want "LVM size may be unitless"
	_ = lvm.ExtendLVOptions{Size: strconv.FormatInt(bytes, 10)} // want "LVM size may be unitless"
	_ = lvm.ExtendLVOptions{
		//lvmsize:ignore Existing call is tracked separately.
		Size: q.String(),
	}
	opts := &lvm.ReduceLVOptions{}
	opts.Size = fmt.Sprintf("+%d", bytes) // want "LVM size may be unitless"
	//lvmsize:ignore
	opts.Size = "123" // want "LVM size may be unitless"
	_ = struct{ Size string }{Size: "123"}
}
