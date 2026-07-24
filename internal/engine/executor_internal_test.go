/*
 * Copyright 2022 The Gremlins Authors
 *
 *    Licensed under the Apache License, Version 2.0 (the "License");
 *    you may not use this file except in compliance with the License.
 *    You may obtain a copy of the License at
 *
 *        http://www.apache.org/licenses/LICENSE-2.0
 *
 *    Unless required by applicable law or agreed to in writing, software
 *    distributed under the License is distributed on an "AS IS" BASIS,
 *    WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *    See the License for the specific language governing permissions and
 *    limitations under the License.
 */

package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/go-gremlins/gremlins/internal/gomodule"
)

const (
	attributionOutput = "output.json"
	jsonFlag          = "-json"
	testAlphaID       = "go:example.com/a:TestAlpha"
	testBetaID        = "go:example.com/b:TestBeta"
)

func TestNewExecutorDealerCollectsAttributionOnlyForCompleteReports(t *testing.T) {
	testCases := map[string]struct {
		output      string
		disableBail bool
		dryRun      bool
		want        bool
	}{
		"ordinary_run":    {},
		"output_only":     {output: attributionOutput},
		"disable_only":    {disableBail: true},
		"complete_report": {output: attributionOutput, disableBail: true, want: true},
		"dry_run":         {output: attributionOutput, disableBail: true, dryRun: true},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := shouldCollectAttribution(tc.dryRun, tc.disableBail, tc.output)
			if got != tc.want {
				t.Errorf("shouldCollectAttribution() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestGetTestArgs(t *testing.T) {
	t.Parallel()

	testCases := map[string]struct {
		buildTags          string
		testExecutionTime  time.Duration
		testCPU            int
		integrationMode    bool
		collectAttribution bool
		disableBail        bool
		pkg                string
		want               []string
	}{
		"should_not_include_tags_flag_when_build_tags_are_empty": {
			testExecutionTime: 10 * time.Second,
			pkg:               "example.com/my/package",
			want:              []string{"test", "-timeout", "12s", "-failfast", "example.com/my/package"},
		},
		"should_include_tags_flag_when_build_tags_are_set": {
			buildTags:         "tag1,tag2",
			testExecutionTime: 10 * time.Second,
			pkg:               "example.com/my/package",
			want:              []string{"test", "-tags", "tag1,tag2", "-timeout", "12s", "-failfast", "example.com/my/package"},
		},
		"should_compute_timeout_as_two_seconds_plus_execution_time": {
			testExecutionTime: 30 * time.Second,
			pkg:               "example.com/my/package",
			want:              []string{"test", "-timeout", "32s", "-failfast", "example.com/my/package"},
		},
		"should_not_include_cpu_flag_when_test_cpu_is_zero": {
			testExecutionTime: 10 * time.Second,
			testCPU:           0,
			pkg:               "example.com/my/package",
			want:              []string{"test", "-timeout", "12s", "-failfast", "example.com/my/package"},
		},
		"should_include_cpu_flag_when_test_cpu_is_nonzero": {
			testExecutionTime: 10 * time.Second,
			testCPU:           4,
			pkg:               "example.com/my/package",
			want:              []string{"test", "-timeout", "12s", "-failfast", "-cpu", "4", "example.com/my/package"},
		},
		"should_use_package_path_when_integration_mode_is_disabled": {
			testExecutionTime: 10 * time.Second,
			integrationMode:   false,
			pkg:               "example.com/my/package",
			want:              []string{"test", "-timeout", "12s", "-failfast", "example.com/my/package"},
		},
		"should_use_dot_dot_dot_path_when_integration_mode_is_enabled": {
			testExecutionTime: 10 * time.Second,
			integrationMode:   true,
			pkg:               "example.com/my/package",
			want:              []string{"test", "-timeout", "12s", "-failfast", "./..."},
		},
		"should_include_all_flags_when_all_options_are_configured": {
			buildTags:         "integration",
			testExecutionTime: 10 * time.Second,
			testCPU:           2,
			integrationMode:   true,
			pkg:               "example.com/my/package",
			want:              []string{"test", "-tags", "integration", "-timeout", "12s", "-failfast", "-cpu", "2", "./..."},
		},
		"should_emit_json_when_collecting_attribution": {
			collectAttribution: true,
			testExecutionTime:  10 * time.Second,
			pkg:                "example.com/my/package",
			want:               []string{"test", "-timeout", "12s", jsonFlag, "-failfast", "example.com/my/package"},
		},
		"should_run_all_tests_when_bail_is_disabled": {
			disableBail:       true,
			testExecutionTime: 10 * time.Second,
			pkg:               "example.com/my/package",
			want:              []string{"test", "-timeout", "12s", "example.com/my/package"},
		},
		"should_collect_all_named_killers": {
			collectAttribution: true,
			disableBail:        true,
			testExecutionTime:  10 * time.Second,
			pkg:                "example.com/my/package",
			want:               []string{"test", "-timeout", "12s", jsonFlag, "example.com/my/package"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sut := &mutantExecutor{
				buildTags:          tc.buildTags,
				testExecutionTime:  tc.testExecutionTime,
				testCPU:            tc.testCPU,
				integrationMode:    tc.integrationMode,
				collectAttribution: tc.collectAttribution,
				disableBail:        tc.disableBail,
			}

			if diff := cmp.Diff(tc.want, sut.getTestArgs(tc.pkg)); diff != "" {
				t.Errorf("getTestArgs() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGetTestInventoryArgs(t *testing.T) {
	t.Parallel()

	testCases := map[string]struct {
		buildTags string
		want      []string
	}{
		"without_build_tags": {
			want: []string{"test", jsonFlag, "-list", "^(Test|Example|Fuzz)", "./..."},
		},
		"with_build_tags": {
			buildTags: "integration,unix",
			want: []string{
				"test", "-tags", "integration,unix",
				jsonFlag, "-list", "^(Test|Example|Fuzz)", "./...",
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sut := &MutantExecutorDealer{buildTags: tc.buildTags}
			if diff := cmp.Diff(tc.want, sut.getTestInventoryArgs()); diff != "" {
				t.Errorf("getTestInventoryArgs() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPrepareAttribution(t *testing.T) {
	t.Run("does nothing when attribution is disabled", func(t *testing.T) {
		sut := &MutantExecutorDealer{
			execContext: func(context.Context, string, ...string) *exec.Cmd {
				t.Fatal("unexpected inventory command")

				return nil
			},
		}
		if err := sut.PrepareAttribution(context.Background()); err != nil {
			t.Fatalf("PrepareAttribution() error = %v", err)
		}
	})

	t.Run("inventories top-level tests", func(t *testing.T) {
		sut := attributionDealer(t, "success")
		if err := sut.PrepareAttribution(context.Background()); err != nil {
			t.Fatalf("PrepareAttribution() error = %v", err)
		}
		want := map[string]struct{}{
			"go:example.com/pkg:TestOne": {},
			"go:example.com/pkg:TestTwo": {},
		}
		if diff := cmp.Diff(want, sut.testInventory); diff != "" {
			t.Errorf("inventory mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(
			[]string{
				"go:example.com/pkg:TestOne",
				"go:example.com/pkg:TestTwo",
			},
			sut.Attribution().Tests,
		); diff != "" {
			t.Errorf("Attribution().Tests mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("returns an inventory command failure", func(t *testing.T) {
		sut := attributionDealer(t, "failure")
		if err := sut.PrepareAttribution(context.Background()); err == nil {
			t.Fatal("PrepareAttribution() error = nil, want command failure")
		}
	})
}

func attributionDealer(t *testing.T, outcome string) *MutantExecutorDealer {
	t.Helper()

	root := t.TempDir()

	return &MutantExecutorDealer{
		collectAttribution: true,
		execContext: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			// #nosec G204 G702 -- the executable and outcome are controlled by this test.
			return exec.CommandContext(
				ctx,
				os.Args[0],
				"-test.run=TestPrepareAttributionProcess",
				"--",
				outcome,
			)
		},
		mod: gomodule.GoModule{Root: root, CallingDir: "."},
		wdDealer: attributionWorkdirStub{
			path: filepath.Join(root, "go-tmp"),
		},
		attribution:   newAttributionStore(),
		testInventory: make(map[string]struct{}),
	}
}

func TestPrepareAttributionProcess(_ *testing.T) {
	outcome := os.Args[len(os.Args)-1]
	if outcome == "success" {
		_, _ = os.Stdout.WriteString(
			`{"Action":"output","Package":"example.com/pkg","Output":"TestTwo\n"}` + "\n" +
				`{"Action":"output","Package":"example.com/pkg","Output":"TestOne\n"}` + "\n",
		)

		return
	}
	if outcome == "failure" {
		os.Exit(1) // skipcq: RVV-A0003
	}
}

type attributionWorkdirStub struct {
	path string
}

func (s attributionWorkdirStub) Get(string) (string, error) { return s.path, nil }
func (attributionWorkdirStub) Clean()                       {}
func (s attributionWorkdirStub) WorkDir() string            { return s.path }

func TestParseGoTestRun(t *testing.T) {
	t.Parallel()

	output := []byte(`
{"Action":"fail","Package":"example.com/b","Test":"TestBeta/subtest"}
{"Action":"fail","Package":"example.com/b","Test":"TestBeta"}
{"Action":"pass","Package":"example.com/a","Test":"TestPassing"}
not-json
{"Action":"fail","Package":"example.com/a","Test":"TestAlpha"}
{"Action":"fail","Package":"example.com/a","Test":"TestAlpha"}
{"Action":"fail","Package":"example.com/package"}
{"Action":"fail","Package":"example.com/broken","FailedBuild":"example.com/broken.test"}
`)
	want := []string{
		testAlphaID,
		testBetaID,
	}

	got := parseGoTestRun(output)
	if diff := cmp.Diff(want, got.failed); diff != "" {
		t.Errorf("failed tests mismatch (-want +got):\n%s", diff)
	}
	wantCompleted := map[string]struct{}{
		testAlphaID:                    {},
		"go:example.com/a:TestPassing": {},
		testBetaID:                     {},
	}
	if diff := cmp.Diff(wantCompleted, got.completed); diff != "" {
		t.Errorf("completed tests mismatch (-want +got):\n%s", diff)
	}
	if !got.failedBuild {
		t.Error("expected build-failure evidence")
	}
}

func TestParseGoTestInventory(t *testing.T) {
	t.Parallel()

	output := []byte(`
{"Action":"output","Package":"example.com/a","Output":"TestAlpha\n"}
{"Action":"output","Package":"example.com/a","Output":"BenchmarkIgnored\n"}
{"Action":"output","Package":"example.com/b","Output":"FuzzBeta\n"}
{"Action":"output","Package":"example.com/b","Test":"TestRunning","Output":"TestOutput\n"}
`)
	want := map[string]struct{}{
		testAlphaID:                 {},
		"go:example.com/b:FuzzBeta": {},
	}

	if diff := cmp.Diff(want, parseGoTestInventory(output)); diff != "" {
		t.Errorf("inventory mismatch (-want +got):\n%s", diff)
	}
}

func TestTestRunCompleted(t *testing.T) {
	t.Parallel()

	inventory := map[string]struct{}{
		testAlphaID: {},
		testBetaID:  {},
	}
	onlyPackageA := map[string]struct{}{testAlphaID: {}}

	completed, complete := testRunProgress(inventory, onlyPackageA, "example.com/a", false)
	if completed != 1 || !complete {
		t.Errorf("selected package progress = (%d, %t), want (1, true)", completed, complete)
	}
	completed, complete = testRunProgress(inventory, onlyPackageA, "example.com/a", true)
	if completed != 1 || complete {
		t.Errorf("integration progress = (%d, %t), want (1, false)", completed, complete)
	}
	completed, complete = testRunProgress(inventory, map[string]struct{}{}, "example.com/missing", false)
	if completed != 0 || complete {
		t.Errorf("absent package progress = (%d, %t), want (0, false)", completed, complete)
	}
}
