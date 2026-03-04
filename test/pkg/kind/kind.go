// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kind

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"local-csi-driver/test/pkg/docker"
	"local-csi-driver/test/pkg/utils"
)

// IsCluster checks if the current cluster is a kind cluster.
func IsCluster() bool {
	cmd := exec.Command("kubectl", "config", "current-context")
	output, err := utils.Run(cmd)
	if err != nil {
		return false
	}
	return strings.Contains(output, "kind-kind")
}

// IsClusterCreated checks if the kind cluster is created.
func IsClusterCreated() bool {
	cmd := exec.Command("kind", "get", "clusters")
	output, err := utils.Run(cmd)
	if err != nil {
		return false
	}
	clusters := utils.GetNonEmptyLines(output)
	return slices.Contains(clusters, "kind")
}

// SetKubeContext sets the kube context to the kind cluster.
func SetKubeContext(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "kind", "export", "kubeconfig", "--name", "kind")
	_, err := utils.Run(cmd)
	return err
}

// LoadImageWithName loads a local docker image to the kind cluster.
func LoadImageWithName(ctx context.Context, name string) error {
	exists, err := docker.CheckImageExists(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to check if image exists: %v", err)
	}
	if !exists {
		err := docker.PullImage(ctx, name)
		if err != nil {
			return fmt.Errorf("failed to pull image: %v", err)
		}
	}
	cluster := "kind"
	if v, ok := os.LookupEnv("KIND_CLUSTER"); ok {
		cluster = v
	}
	kindOptions := []string{"load", "docker-image", name, "--name", cluster}
	cmd := exec.CommandContext(ctx, "kind", kindOptions...)
	_, err = utils.Run(cmd)
	return err
}
