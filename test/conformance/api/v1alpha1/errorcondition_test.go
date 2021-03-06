// +build e2e

/*
Copyright 2018 The Knative Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ptest "knative.dev/pkg/test"
	"knative.dev/serving/pkg/apis/serving/v1alpha1"
	serviceresourcenames "knative.dev/serving/pkg/reconciler/service/resources/names"
	v1a1opts "knative.dev/serving/pkg/testing/v1alpha1"
	"knative.dev/serving/test"
	v1a1test "knative.dev/serving/test/v1alpha1"
)

const (
	containerMissing = "ContainerMissing"
)

// TestContainerErrorMsg is to validate the error condition defined at
// https://github.com/knative/docs/blob/master/docs/serving/spec/knative-api-specification-1.0.md#error-signalling
// for the container image missing scenario.
func TestContainerErrorMsg(t *testing.T) {
	t.Parallel()
	if strings.HasSuffix(strings.Split(ptest.Flags.DockerRepo, "/")[0], ".local") {
		t.Skip("Skipping for local docker repo")
	}
	clients := test.Setup(t)

	names := test.ResourceNames{
		Service: test.ObjectNameForTest(t),
		Image:   test.InvalidHelloWorld,
	}

	test.EnsureTearDown(t, clients, names)

	// Specify an invalid image path
	// A valid DockerRepo is still needed, otherwise will get UNAUTHORIZED instead of container missing error
	t.Logf("Creating a new Service %s", names.Service)
	svc, err := v1a1test.CreateLatestService(t, clients, names)
	if err != nil {
		t.Fatal("Failed to create Service:", err)
	}
	names.Config = serviceresourcenames.Configuration(svc)
	names.Route = serviceresourcenames.Route(svc)

	manifestUnknown := string(transport.ManifestUnknownErrorCode)
	t.Log("When the imagepath is invalid, the Configuration should have error status.")

	// Wait for ServiceState becomes NotReady. It also waits for the creation of Configuration.
	if err := v1a1test.WaitForServiceState(clients.ServingAlphaClient, names.Service, v1a1test.IsServiceAndChildrenFailed, "ServiceIsNotReady"); err != nil {
		t.Fatalf("The Service %s was unexpected state: %v", names.Service, err)
	}

	// Checking for "Container image not present in repository" scenario defined in error condition spec
	err = v1a1test.CheckConfigurationState(clients.ServingAlphaClient, names.Config, func(r *v1alpha1.Configuration) (bool, error) {
		cond := r.Status.GetCondition(v1alpha1.ConfigurationConditionReady)
		if cond != nil && !cond.IsUnknown() {
			if strings.Contains(cond.Message, manifestUnknown) && cond.IsFalse() {
				return true, nil
			}
			t.Logf("Reason: %s ; Message: %s ; Status %s", cond.Reason, cond.Message, cond.Status)
			return true, fmt.Errorf("The configuration %s was not marked with expected error condition (Reason=%q, Message=%q, Status=%q), but with (Reason=%q, Message=%q, Status=%q)",
				names.Config, containerMissing, manifestUnknown, "False", cond.Reason, cond.Message, cond.Status)
		}
		return false, nil
	})

	if err != nil {
		t.Fatal("Failed to validate configuration state:", err)
	}

	revisionName, err := getRevisionFromConfiguration(clients, names.Config)
	if err != nil {
		t.Fatalf("Failed to get revision from configuration %s: %v", names.Config, err)
	}

	t.Log("When the imagepath is invalid, the revision should have error status.")
	err = v1a1test.CheckRevisionState(clients.ServingAlphaClient, revisionName, func(r *v1alpha1.Revision) (bool, error) {
		cond := r.Status.GetCondition(v1alpha1.RevisionConditionReady)
		if cond != nil {
			if cond.Reason == containerMissing && strings.Contains(cond.Message, manifestUnknown) {
				return true, nil
			}
			return true, fmt.Errorf("The revision %s was not marked with expected error condition (Reason=%q, Message=%q), but with (Reason=%q, Message=%q)",
				revisionName, containerMissing, manifestUnknown, cond.Reason, cond.Message)
		}
		return false, nil
	})

	if err != nil {
		t.Fatal("Failed to validate revision state:", err)
	}

	t.Log("When the revision has error condition, route should not be ready.")
	err = v1a1test.CheckRouteState(clients.ServingAlphaClient, names.Route, v1a1test.IsRouteNotReady)
	if err != nil {
		t.Fatalf("the Route %s was not desired state: %v", names.Route, err)
	}
}

// TestContainerExitingMsg is to validate the error condition defined at
// https://github.com/knative/serving/blob/master/docs/spec/errors.md
// for the container crashing scenario.
func TestContainerExitingMsg(t *testing.T) {
	t.Parallel()
	const (
		// The given image will always exit with an exit code of 5
		exitCodeReason = "ExitCode5"
		// ... and will print "Crashed..." before it exits
		errorLog = "Crashed..."
	)

	tests := []struct {
		Name           string
		ReadinessProbe *corev1.Probe
	}{{
		Name: "http",
		ReadinessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				HTTPGet: &corev1.HTTPGetAction{},
			},
		},
	}, {
		Name: "tcp",
		ReadinessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				TCPSocket: &corev1.TCPSocketAction{},
			},
		},
	}}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.Name, func(t *testing.T) {
			t.Parallel()
			clients := test.Setup(t)

			names := test.ResourceNames{
				Config: test.ObjectNameForTest(t),
				Image:  test.Failing,
			}

			test.EnsureTearDown(t, clients, names)

			t.Logf("Creating a new Configuration %s", names.Config)

			if _, err := v1a1test.CreateConfiguration(t, clients, names, v1a1opts.WithConfigReadinessProbe(tt.ReadinessProbe)); err != nil {
				t.Fatalf("Failed to create configuration %s: %v", names.Config, err)
			}

			t.Log("When the containers keep crashing, the Configuration should have error status.")

			err := v1a1test.WaitForConfigurationState(clients.ServingAlphaClient, names.Config, func(r *v1alpha1.Configuration) (bool, error) {
				cond := r.Status.GetCondition(v1alpha1.ConfigurationConditionReady)
				if cond != nil && !cond.IsUnknown() {
					if strings.Contains(cond.Message, errorLog) && cond.IsFalse() {
						return true, nil
					}
					t.Logf("Reason: %s ; Message: %s ; Status: %s", cond.Reason, cond.Message, cond.Status)
					return true, fmt.Errorf("The configuration %s was not marked with expected error condition (Reason=%q, Message=%q, Status=%q), but with (Reason=%q, Message=%q, Status=%q)",
						names.Config, containerMissing, errorLog, "False", cond.Reason, cond.Message, cond.Status)
				}
				return false, nil
			}, "ConfigContainersCrashing")

			if err != nil {
				t.Fatal("Failed to validate configuration state:", err)
			}

			revisionName, err := getRevisionFromConfiguration(clients, names.Config)
			if err != nil {
				t.Fatalf("Failed to get revision from configuration %s: %v", names.Config, err)
			}

			t.Log("When the containers keep crashing, the revision should have error status.")
			err = v1a1test.WaitForRevisionState(clients.ServingAlphaClient, revisionName, func(r *v1alpha1.Revision) (bool, error) {
				cond := r.Status.GetCondition(v1alpha1.RevisionConditionReady)
				if cond != nil {
					if cond.Reason == exitCodeReason && strings.Contains(cond.Message, errorLog) {
						return true, nil
					}
					return true, fmt.Errorf("The revision %s was not marked with expected error condition (Reason=%q, Message=%q), but with (Reason=%q, Message=%q)",
						revisionName, exitCodeReason, errorLog, cond.Reason, cond.Message)
				}
				return false, nil
			}, "RevisionContainersCrashing")

			if err != nil {
				t.Fatal("Failed to validate revision state:", err)
			}
		})
	}
}

// Get revision name from configuration.
func getRevisionFromConfiguration(clients *test.Clients, configName string) (string, error) {
	config, err := clients.ServingAlphaClient.Configs.Get(configName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if config.Status.LatestCreatedRevisionName != "" {
		return config.Status.LatestCreatedRevisionName, nil
	}
	return "", fmt.Errorf("No valid revision name found in configuration %s", configName)
}
