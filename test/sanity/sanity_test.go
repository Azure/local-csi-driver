// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sanity

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubernetes-csi/csi-test/v5/pkg/sanity"

	"local-csi-driver/internal/pkg/retry"
	"local-csi-driver/test/pkg/utils"
)

const (
	// targetPath is the path where the volume during node publish in the sanity tests.
	targetPath = "/tmp/csi-mount"
	// stagingPath is the path where the volume is staged in the sanity tests.
	stagingPath = "/tmp/csi-staging"
	// volumeGroupName is the name of the volume group used in the sanity tests.
	volumeGroupName = "lcd-vg"
)

// testConfig is a configuration for a sanity test.
type testConfig struct {
	// testSuiteName is the name of the test suite
	testSuiteName string
	// labels are the labels for the test
	labels Labels
	// testVolumeParameters are the volume parameters for the test
	testVolumeParameters func() (map[string]string, error)
	// controllerAddressPort is the address of the csi-controller plugin after port forwarding
	controllerAddressPort string
	// socatPort is the port on which the socat is listening for the socket in agent or manager
	socatPort string
	// skips are the test patterns that are not supported by the driver
	skips []string
}

// TestConfigs is a list of test configurations to run.
var TestConfigs = []testConfig{
	{
		testSuiteName: "lvm CSI Sanity Suite",
		labels:        Label("aks", "lvm", "sanity"),
		testVolumeParameters: func() (map[string]string, error) {
			return map[string]string{
				"localdisk.csi.acstor.io/vg": volumeGroupName,
			}, nil
		},
		controllerAddressPort: "10001",
		socatPort:             "10000",
		skips: []string{
			// no intent to fix, we would need to decouple LV name from PV name
			"CreateVolume should not fail when creating volume with maximum-length name",
		},
	},
}

// SetupPortForwarding forwards the csi-node and csi-controller ports.
func (t *testConfig) SetupPortForwarding(ctx context.Context) {

	components := []struct {
		// resource to port-forward
		resource string
		// port to port-forward
		port string
	}{
		{controllerDs, t.controllerAddressPort},
	}

	for _, component := range components {
		t.portForward(component.resource, component.port, t.socatPort)
	}
}

// portForward starts port-forwarding for a resource and registers a cleanup function.
func (t *testConfig) portForward(resource string, hostPort string, containerPort string) {
	By("Starting port-forwarding for " + resource)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer GinkgoRecover()
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				_, _ = fmt.Fprintf(GinkgoWriter, "Context is done, stopping port-forwarding for %s\n", resource)
				return
			default:
				cmd := exec.CommandContext(ctx, "kubectl", "port-forward", "-n", namespace, resource, hostPort+":"+containerPort)
				if _, err := utils.Run(cmd); err != nil {
					_, _ = fmt.Fprintf(GinkgoWriter, "Failed to start port-forwarding for %s: %v\n", resource, err)
				}
				if ctx.Err() != nil {
					time.Sleep(1 * time.Second)
				}
			}
		}
	}()

	DeferCleanup(func() {
		By("Stopping port-forwarding for " + resource)
		cancel()
		Eventually(done).Should(BeClosed(), "Port-forwarding for %s did not stop", resource)
	})
}

// RegisterTests registers the CSI sanity tests.
func (t *testConfig) RegisterTests() {
	var _ = Context(t.testSuiteName, t.labels, func() {
		BeforeEach(func() {
			t.SkipUnsupportedTest()
		})
		sanityConfig := sanity.NewTestConfig()
		sanityConfig.Address = "localhost:" + t.controllerAddressPort
		sanityConfig.TargetPath = targetPath
		sanityConfig.StagingPath = stagingPath
		sanityConfig.CreateStagingDir = mkdirInPod
		sanityConfig.CreateTargetDir = mkdirInPod
		sanityConfig.RemoveStagingPath = removePathInPod
		sanityConfig.RemoveTargetPath = removePathInPod
		sanityConfig.CheckPath = checkPathInPod
		idGen, err := NewLVMGenerator(volumeGroupName)
		if err != nil {
			panic("Failed to create ID generator: " + err.Error())
		}
		sanityConfig.IDGen = idGen

		// setup test volume parameters
		sanityConfig.TestVolumeParameters, err = t.testVolumeParameters()
		if err != nil {
			panic("Failed to get test volume parameters: " + err.Error())
		}
		dialOptions := []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithAuthority("localhost"),
			grpc.WithUnaryInterceptor(retryUnaryClientInterceptor(10, 3*time.Minute, time.Second)),
		}
		sanityConfig.DialOptions = dialOptions
		sanityConfig.ControllerDialOptions = dialOptions
		sanity.GinkgoTest(&sanityConfig)
	})
}

// retryUnaryClientInterceptor returns a new unary client interceptor that performs retries on transient errors.
func retryUnaryClientInterceptor(maxRetries int, timeout time.Duration, backoff time.Duration) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		var err error
		retryBackoff := wait.Backoff{
			Duration: backoff,
			Factor:   1.2,
			Jitter:   0.1,
			Steps:    maxRetries,
		}
		// sanity tests pass in context.Background() into requests, if we hang on the request
		// we will just wait for test suite to timeout, so we need to set a timeout
		// on the context to avoid hanging forever
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		err = retry.OnError(retryBackoff, func(err error) bool {
			st, ok := status.FromError(err)
			isRetry := ok && isTransientError(st.Code())
			if isRetry {
				_, _ = fmt.Fprintf(GinkgoWriter, " %s retrying: %v\n", method, err)
			}
			return isRetry
		}, func() error {
			if err := invoker(ctx, method, req, reply, cc, opts...); err != nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "method: %s, request: %v, error: %v\n", method, req, err)
				return err
			}
			_, _ = fmt.Fprintf(GinkgoWriter, "method: %s, request: %v, response: %v\n", method, req, reply)
			return nil
		})
		return err
	}
}

// isTransientError checks if the given gRPC error code is transient and should be retried.
func isTransientError(code codes.Code) bool {
	switch code {
	case codes.Unavailable, codes.ResourceExhausted:
		return true
	default:
		return false
	}
}

// MatchesLabelFilter checks if the test configuration matches the label filter
// We are using the GinkgoLabelFilter to filter the tests, this helps us decide
// when we need to run the setup for lvm, lvm-annotation, or both.
func (t *testConfig) MatchesLabelFilter() bool {
	return t.labels.MatchesLabelFilter(GinkgoLabelFilter())
}

func (t *testConfig) SkipUnsupportedTest() {
	for _, skip := range t.skips {
		if strings.Contains(CurrentSpecReport().FullText(), skip) {
			Skip(fmt.Sprintf("Test pattern %s is not supported by driver %v", skip, t.labels))
		}
	}
}

// mkdirInPod creates a directory in the csi-local-node pod.
func mkdirInPod(path string) (string, error) {
	procPath := fmt.Sprintf("%s-%d", path, GinkgoParallelProcess())
	_, err := execInPod("mkdir", "-p", procPath)
	if err != nil {
		return "", err
	}
	return procPath, nil
}

// removePathInPod removes a path in the csi-local-node pod.
func removePathInPod(path string) error {
	_, err := execInPod("rm", "-rf", path)
	return err
}

// checkPathInPod checks the type of path in the csi-local-node pod.
func checkPathInPod(path string) (sanity.PathKind, error) {
	output, err := execInPod("sh", "-c", "if [ -f "+path+" ]; then echo file; elif [ -d "+path+" ]; then echo directory; elif [ ! -e "+path+" ]; then echo not_found; else echo other; fi")
	if err != nil {
		return "", err
	}
	switch strings.TrimSpace(output) {
	case "file":
		return sanity.PathIsFile, nil
	case "directory":
		return sanity.PathIsDir, nil
	case "not_found":
		return sanity.PathIsNotFound, nil
	default:
		return sanity.PathIsOther, nil
	}
}

// execInPod executes a command in the cns-node-agent pod.
func execInPod(args ...string) (string, error) {
	var output string
	backoff := wait.Backoff{
		Duration: 1 * time.Second,
		Factor:   2.0,
		Jitter:   0.1,
		Steps:    5,
	}
	err := retry.OnError(backoff, func(err error) bool {
		return true
	}, func() error {
		var err error
		cmd := exec.Command("kubectl", append([]string{"exec", "-n", namespace, controllerDs, "--"}, args...)...)
		output, err = utils.Run(cmd)
		return err
	})
	return output, err
}
