// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package gc

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// hasNodeAnnotationMismatch checks if the PV's node annotations don't match the current node.
func hasNodeAnnotationMismatch(pv *corev1.PersistentVolume, nodeID, selectedNodeAnnotation, selectedInitialNodeParam string) bool {
	// Check selected-node annotation
	selectedNode, exists := pv.Annotations[selectedNodeAnnotation]
	if exists {
		if !strings.EqualFold(selectedNode, nodeID) {
			return true
		}
	} else {
		// Check initial node parameter in CSI volume attributes
		if pv.Spec.CSI != nil && pv.Spec.CSI.VolumeAttributes != nil {
			if initialNode, exists := pv.Spec.CSI.VolumeAttributes[selectedInitialNodeParam]; exists {
				if !strings.EqualFold(initialNode, nodeID) {
					return true
				}
			}
		}
	}

	return false
}

// parseVolumeID parses a volume ID in the format <volume-group>#<logical-volume>
// and returns the volume group name and logical volume name.
func parseVolumeID(volumeID string) (vgName, lvName string, err error) {
	const separator = "#"
	segments := strings.Split(volumeID, separator)
	if len(segments) != 2 {
		return "", "", fmt.Errorf("error parsing volume id: %q, expected 2 segments, got %d", volumeID, len(segments))
	}

	vgName = segments[0]
	lvName = segments[1]

	if len(vgName) == 0 {
		return "", "", fmt.Errorf("error parsing volume id: %q, volume group name is empty", volumeID)
	}
	if len(lvName) == 0 {
		return "", "", fmt.Errorf("error parsing volume id: %q, logical volume name is empty", volumeID)
	}

	return vgName, lvName, nil
}
