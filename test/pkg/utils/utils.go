// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package utils

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
)

const (
	prometheusOperatorVersion = "v0.77.1"
	prometheusOperatorURL     = "https://github.com/prometheus-operator/prometheus-operator/" +
		"releases/download/%s/bundle.yaml"
)

func warnError(err error) {
	_, _ = fmt.Fprintf(GinkgoWriter, "warning: %v\n", err)
}

// Run executes the provided command within this context.
func Run(cmd *exec.Cmd) (string, error) {
	dir, _ := GetProjectDir()
	cmd.Dir = dir
	if err := os.Chdir(cmd.Dir); err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "chdir dir: %s\n", err)
	}

	cmd.Env = append(os.Environ(), "GO111MODULE=on")
	command := strings.Join(cmd.Args, " ")
	_, _ = fmt.Fprintf(GinkgoWriter, "running: %s\n", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "%s failed with error: (%v) %s\n", command, err, string(output))
		return string(output), err
	}

	return string(output), nil
}

func RunOutput(cmd *exec.Cmd) (string, error) {
	dir, _ := GetProjectDir()
	cmd.Dir = dir
	if err := os.Chdir(cmd.Dir); err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "chdir dir: %s\n", err)
	}

	cmd.Env = append(os.Environ(), "GO111MODULE=on")
	command := strings.Join(cmd.Args, " ")
	_, _ = fmt.Fprintf(GinkgoWriter, "running: %s\n", command)
	output, err := cmd.Output()
	if err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "%s failed with error: (%v) %s\n", command, err, string(output))
		return string(output), err
	}

	return string(output), nil
}

// InstallPrometheusOperator installs the prometheus Operator to be used to
// export the enabled metrics.
func InstallPrometheusOperator(ctx context.Context) error {
	url := fmt.Sprintf(prometheusOperatorURL, prometheusOperatorVersion)
	cmd := exec.CommandContext(ctx, "kubectl", "create", "-f", url)
	_, err := Run(cmd)
	return err
}

// UninstallPrometheusOperator uninstalls prometheus.
func UninstallPrometheusOperator(ctx context.Context) {
	url := fmt.Sprintf(prometheusOperatorURL, prometheusOperatorVersion)
	cmd := exec.CommandContext(ctx, "kubectl", "delete", "-f", url, "--ignore-not-found")
	if _, err := Run(cmd); err != nil {
		warnError(err)
	}
}

// IsPrometheusCRDsInstalled checks if any Prometheus CRDs are installed
// by verifying the existence of key CRDs related to Prometheus.
func IsPrometheusCRDsInstalled(ctx context.Context) bool {
	// List of common Prometheus CRDs.
	prometheusCRDs := []string{
		"prometheuses.monitoring.coreos.com",
		"prometheusrules.monitoring.coreos.com",
		"prometheusagents.monitoring.coreos.com",
	}

	cmd := exec.CommandContext(ctx, "kubectl", "get", "crds", "-o", "custom-columns=NAME:.metadata.name")
	output, err := Run(cmd)
	if err != nil {
		return false
	}
	crdList := GetNonEmptyLines(output)
	for _, crd := range prometheusCRDs {
		for _, line := range crdList {
			if strings.Contains(line, crd) {
				return true
			}
		}
	}

	return false
}

// GetNonEmptyLines converts given command output string into individual objects
// according to line breakers, and ignores the empty elements in it.
func GetNonEmptyLines(output string) []string {
	var res []string
	elements := strings.SplitSeq(output, "\n")
	for element := range elements {
		if element != "" {
			res = append(res, element)
		}
	}

	return res
}

// GetProjectDir will return the directory where the project is.
func GetProjectDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return wd, err
	}
	wd = strings.ReplaceAll(wd, "/test/e2e", "")
	wd = strings.ReplaceAll(wd, "/test/external", "")
	wd = strings.ReplaceAll(wd, "/test/sanity", "")
	wd = strings.ReplaceAll(wd, "/test/scale", "")
	wd = strings.ReplaceAll(wd, "/test/aks", "")
	return wd, nil
}

// RandomTag returns a random tag.
func RandomTag() string {
	bytes := make([]byte, 4)
	if _, err := rand.Read(bytes); err != nil {
		return ""
	}
	return hex.EncodeToString(bytes)
}

// UncommentCode searches for target in the file and remove the comment prefix
// of the target content. The target content may span multiple lines.
func UncommentCode(filename, target, prefix string) error {
	// false positive
	//nolint:gosec
	content, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	strContent := string(content)

	idx := strings.Index(strContent, target)
	if idx < 0 {
		return fmt.Errorf("unable to find the code %s to be uncomment", target)
	}

	out := new(bytes.Buffer)
	_, err = out.Write(content[:idx])
	if err != nil {
		return err
	}

	scanner := bufio.NewScanner(bytes.NewBufferString(target))
	if !scanner.Scan() {
		return nil
	}
	for {
		_, err := out.WriteString(strings.TrimPrefix(scanner.Text(), prefix))
		if err != nil {
			return err
		}
		// Avoid writing a newline in case the previous line was the last in target.
		if !scanner.Scan() {
			break
		}
		if _, err := out.WriteString("\n"); err != nil {
			return err
		}
	}

	_, err = out.Write(content[idx+len(target):])
	if err != nil {
		return err
	}
	// false positive
	//nolint:gosec
	return os.WriteFile(filename, out.Bytes(), 0644)
}

// CollectSupportBundle runs the make get-support-bundle command with the
// SUPPORT_BUNDLE_SINCE_TIME environment variable set.
func CollectSupportBundle(ctx context.Context, outputFile string, startTime time.Time) (string, error) {
	// Format the startTime to the required format
	sinceTime := startTime.Format(time.RFC3339)

	// Set the SUPPORT_BUNDLE_SINCE_TIME environment variable
	cmd := exec.CommandContext(ctx, "make", "get-support-bundle")
	cmd.Env = append(os.Environ(), "SUPPORT_BUNDLE_SINCE_TIME="+sinceTime, "SUPPORT_BUNDLE_OUTPUT="+outputFile)

	// Run the command
	output, err := cmd.Output()
	if err != nil {
		return string(output), err
	}

	return string(output), nil
}

// Sleep sleeps for the given duration or until the context is done.
// Useful for our tests where we might want to be able to cancel the
// test during the sleep.
func Sleep(ctx context.Context, delay time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}
