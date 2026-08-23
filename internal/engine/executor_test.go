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

package engine_test

import (
	"context"
	"fmt"
	"go/token"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/go-gremlins/gremlins/internal/configuration"
	"github.com/go-gremlins/gremlins/internal/engine"
	"github.com/go-gremlins/gremlins/internal/engine/workerpool"
	"github.com/go-gremlins/gremlins/internal/gomodule"
	"github.com/go-gremlins/gremlins/internal/mutator"
)

func TestApplyAndRollback(t *testing.T) {
	t.Run("applies and rolls back", func(t *testing.T) {
		wdDealer := newWdDealerStub(t)
		tmpDir, _ := wdDealer.Get("")
		mod := gomodule.GoModule{
			Name:       "example.com",
			Root:       tmpDir,
			CallingDir: ".",
		}
		mjd := engine.NewExecutorDealer(mod, wdDealer, expectedTimeout, engine.WithExecContext(fakeExecCommandSuccess))
		mut := &mutantStub{
			status:  mutator.Runnable,
			mutType: mutator.ConditionalsBoundary,
			pkg:     "example.com",
		}
		outCh := make(chan mutator.Mutator)
		wg := sync.WaitGroup{}
		wg.Add(1)
		executor := mjd.NewExecutor(mut, outCh, &wg)
		w := &workerpool.Worker{
			Name: "test",
			ID:   1,
		}
		go func() {
			<-outCh
			close(outCh)
		}()

		executor.Start(w)

		wg.Wait()

		if !mut.applyCalled {
			t.Errorf("expected apply to be called")
		}

		if !mut.rollbackCalled {
			t.Errorf("expected rollback to be called")
		}
	})

	t.Run("does nothing if apply goes to error", func(t *testing.T) {
		wdDealer := newWdDealerStub(t)
		tmpDir, _ := wdDealer.Get("")
		mod := gomodule.GoModule{
			Name:       "example.com",
			Root:       tmpDir,
			CallingDir: ".",
		}
		mjd := engine.NewExecutorDealer(mod, wdDealer, expectedTimeout, engine.WithExecContext(fakeExecCommandSuccess))
		mut := &mutantStub{
			status:        mutator.Runnable,
			mutType:       mutator.ConditionalsBoundary,
			pkg:           "example.com",
			hasApplyError: true,
		}
		outCh := make(chan mutator.Mutator)
		wg := sync.WaitGroup{}
		wg.Add(1)
		executor := mjd.NewExecutor(mut, outCh, &wg)
		w := &workerpool.Worker{
			Name: "test",
			ID:   1,
		}
		go func() {
			<-outCh
			close(outCh)
		}()

		executor.Start(w)

		wg.Wait()

		if !mut.applyCalled {
			t.Errorf("expected apply to be called")
		}

		if mut.rollbackCalled {
			t.Errorf("expected rollback not to be called")
		}
	})
}

// A mutant that could not be applied must still be in the report.
//
// Dropping it shrinks the corpus without saying so: the run then describes
// fewer mutants than the analysis found, and nothing distinguishes "we
// measured this and it survived" from "we never managed to write it". A full
// disk did exactly that to thousands of mutants at once.
func TestAMutantThatCannotBeAppliedIsReportedNotDropped(t *testing.T) {
	wdDealer := newWdDealerStub(t)
	tmpDir, _ := wdDealer.Get("")
	mod := gomodule.GoModule{
		Name:       "example.com",
		Root:       tmpDir,
		CallingDir: ".",
	}
	mjd := engine.NewExecutorDealer(mod, wdDealer, expectedTimeout, engine.WithExecContext(fakeExecCommandSuccess))
	mut := &mutantStub{
		status:        mutator.Runnable,
		mutType:       mutator.ConditionalsBoundary,
		pkg:           "example.com",
		hasApplyError: true,
	}
	outCh := make(chan mutator.Mutator, 1)
	wg := sync.WaitGroup{}
	wg.Add(1)
	executor := mjd.NewExecutor(mut, outCh, &wg)

	executor.Start(&workerpool.Worker{Name: "test", ID: 1})
	wg.Wait()
	close(outCh)

	reported, ok := <-outCh
	if !ok {
		t.Fatal("the mutant left the corpus entirely instead of being reported")
	}
	if reported.Status() != mutator.Skipped {
		t.Errorf("status = %v, want %v: an unapplied mutant was not measured", reported.Status(), mutator.Skipped)
	}
}

type execContext = func(ctx context.Context, name string, args ...string) *exec.Cmd

func TestMutatorTestExecution(t *testing.T) {
	testCases := []struct {
		testResult    execContext
		name          string
		mutantStatus  mutator.Status
		wantMutStatus mutator.Status
	}{
		{
			name:          "it skips NOT_COVERED",
			testResult:    fakeExecCommandSuccess,
			mutantStatus:  mutator.NotCovered,
			wantMutStatus: mutator.NotCovered,
		},
		{
			name:          "if tests pass then mutation is LIVED",
			testResult:    fakeExecCommandSuccess,
			mutantStatus:  mutator.Runnable,
			wantMutStatus: mutator.Lived,
		},
		{
			name:          "if tests fails then mutation is KILLED",
			testResult:    fakeExecCommandTestsFailure,
			mutantStatus:  mutator.Runnable,
			wantMutStatus: mutator.Killed,
		},
		{
			name:          "if build fails then mutation is BUILD FAILED",
			testResult:    fakeExecCommandBuildFailure,
			mutantStatus:  mutator.Runnable,
			wantMutStatus: mutator.NotViable,
		},
	}
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			viperSet(map[string]any{configuration.UnleashDryRunKey: false})
			defer viperReset()
			wdDealer := newWdDealerStub(t)
			holder := &commandHolder{}
			mod := gomodule.GoModule{
				Name:       "example.com",
				Root:       ".",
				CallingDir: ".",
			}
			mjd := engine.NewExecutorDealer(mod, wdDealer, expectedTimeout,
				engine.WithExecContext(fakeExecCommandWithHolder(holder, tc.testResult)),
			)
			mut := &mutantStub{
				status:  tc.mutantStatus,
				mutType: mutator.ConditionalsBoundary,
				pkg:     "example.com",
			}
			outCh := make(chan mutator.Mutator)
			wg := sync.WaitGroup{}
			wg.Add(1)
			executor := mjd.NewExecutor(mut, outCh, &wg)
			w := &workerpool.Worker{
				Name: "test",
				ID:   1,
			}

			var got mutator.Mutator
			mutex := sync.RWMutex{}
			go func() {
				mutex.Lock()
				defer mutex.Unlock()
				got = <-outCh
				close(outCh)
			}()
			executor.Start(w)
			wg.Wait()

			mutex.RLock()
			defer mutex.RUnlock()

			if got.Status() != tc.wantMutStatus {
				t.Errorf("expected mutation to be %v, but got: %v", tc.wantMutStatus, got.Status())
			}

			if tc.mutantStatus != mutator.NotCovered {
				goTmpDirEnv := fmt.Sprintf("GOTMPDIR=%s", wdDealer.WorkDir())
				actualGoTmpDir := ""
				for _, v := range holder.cmd.Env {
					if strings.HasPrefix(v, "GOTMPDIR=") {
						actualGoTmpDir = v

						break
					}
				}
				if goTmpDirEnv != actualGoTmpDir {
					t.Errorf("expected GOTMPDIR to be %s, got %s", goTmpDirEnv, actualGoTmpDir)
				}
			}
		})
	}
}

const expectedTimeout = 10 * time.Second

type commandHolder struct {
	cmd     *exec.Cmd
	command string
	args    []string
	timeout time.Duration
	m       sync.Mutex
}

func TestMutatorRun(t *testing.T) {
	testCases := []struct {
		name               string
		pkg                string
		callDir            string
		tags               string
		wantPath           string
		timeoutCoefficient int
		intMode            bool
	}{
		{
			name:     "normal mode",
			intMode:  false,
			pkg:      "example.com/my/package",
			callDir:  "test/dir",
			tags:     "tag1,t1g2",
			wantPath: "example.com/my/package",
		},
		{
			name:     "integration mode",
			intMode:  true,
			pkg:      "example.com/my/package",
			callDir:  "test/dir",
			tags:     "tag1,t1g2",
			wantPath: "./...",
		},
		{
			name:               "it can override timeout coefficient",
			timeoutCoefficient: 4,
			pkg:                "example.com/my/package",
			callDir:            "test/dir",
			tags:               "tag1,t1g2",
			wantPath:           "example.com/my/package",
		},
	}
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			settings := map[string]any{
				configuration.UnleashIntegrationMode: tc.intMode,
				configuration.UnleashTagsKey:         tc.tags,
			}
			if tc.timeoutCoefficient != 0 {
				settings[configuration.UnleashTimeoutCoefficientKey] = tc.timeoutCoefficient
			}
			viperSet(settings)
			defer viperReset()

			mod := gomodule.GoModule{
				Name:       "example.com",
				Root:       ".",
				CallingDir: tc.callDir,
			}
			wdDealer := newWdDealerStub(t)
			holder := &commandHolder{}
			mjd := engine.NewExecutorDealer(mod, wdDealer, expectedTimeout,
				engine.WithExecContext(fakeExecCommandSuccessWithHolder(holder)))
			mut := &mutantStub{
				status:  mutator.Runnable,
				mutType: mutator.ConditionalsBoundary,
				pkg:     tc.pkg,
			}
			outCh := make(chan mutator.Mutator)
			wg := sync.WaitGroup{}
			wg.Add(1)
			executor := mjd.NewExecutor(mut, outCh, &wg)
			w := &workerpool.Worker{
				Name: "test",
				ID:   1,
			}
			go func() {
				<-outCh
				close(outCh)
			}()
			executor.Start(w)
			wg.Wait()

			wantTimeout := 2*time.Second + expectedTimeout*engine.DefaultTimeoutCoefficient
			if tc.timeoutCoefficient != 0 {
				wantTimeout = 2*time.Second + expectedTimeout*time.Duration(tc.timeoutCoefficient)
			}
			want := fmt.Sprintf("go test -tags %s -timeout %s -failfast %s", tc.tags, wantTimeout, tc.wantPath)
			got := fmt.Sprintf("go %v", strings.Join(holder.args, " "))

			if !cmp.Equal(got, want) {
				t.Errorf("\n+ %s\n- %s\n", got, want)
			}

			timeoutDifference := absTimeDiff(holder.timeout, expectedTimeout*2)
			diffThreshold := 100 * time.Second
			if timeoutDifference > diffThreshold {
				t.Errorf("expected timeout to be within %s from the set timeout, got %s", diffThreshold, timeoutDifference)
			}
		})
	}
}

func TestCPU(t *testing.T) {
	testCases := []struct {
		name        string
		testCPU     int
		wantTestCPU int
		intMode     bool
		cpuPresent  bool
	}{
		{
			name:       "default normal mode doesn't set CPU",
			cpuPresent: false,
		},
		{
			name:       "default integration mode doesn't set CPU",
			intMode:    true,
			cpuPresent: false,
		},
		{
			name:        "normal mode can override CPU",
			testCPU:     1,
			wantTestCPU: 1,
			cpuPresent:  true,
		},
		{
			name:        "integration mode overrides CPU to half",
			intMode:     true,
			testCPU:     2,
			wantTestCPU: 1,
			cpuPresent:  true,
		},
	}
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			viperSet(map[string]any{
				configuration.UnleashIntegrationMode: tc.intMode,
				configuration.UnleashTestCPUKey:      tc.testCPU,
			})
			defer viperReset()

			mod := gomodule.GoModule{
				Name:       "example.com",
				Root:       ".",
				CallingDir: ".",
			}
			wdDealer := newWdDealerStub(t)
			holder := &commandHolder{}
			mjd := engine.NewExecutorDealer(mod, wdDealer, expectedTimeout,
				engine.WithExecContext(fakeExecCommandSuccessWithHolder(holder)))
			mut := &mutantStub{
				status:  mutator.Runnable,
				mutType: mutator.ConditionalsBoundary,
				pkg:     "test",
			}
			outCh := make(chan mutator.Mutator)
			wg := sync.WaitGroup{}
			wg.Add(1)
			executor := mjd.NewExecutor(mut, outCh, &wg)
			w := &workerpool.Worker{
				Name: "test",
				ID:   1,
			}
			go func() {
				<-outCh
				close(outCh)
			}()
			executor.Start(w)
			wg.Wait()

			for _, arg := range holder.args {
				if !tc.cpuPresent && strings.Contains(arg, "-cpu") {
					t.Fatalf("didn't expect to have -cpu flag")
				}
				if !tc.cpuPresent {
					return
				}
				got := fmt.Sprintf("go %v", strings.Join(holder.args, " "))
				cpuFlag := fmt.Sprintf("-cpu %d", tc.wantTestCPU)
				if strings.Contains(got, cpuFlag) {
					// PASS
					return
				}
				t.Fatalf("want flag %q, got args %s", cpuFlag, holder.args)
			}

		})
	}
}

func absTimeDiff(a, b time.Duration) time.Duration {
	if a > b {
		return a - b
	}

	return b - a
}

func TestCoverageProcessSuccess(_ *testing.T) {
	if os.Getenv("GO_TEST_PROCESS") != "1" {
		return
	}
	os.Exit(0) // skipcq: RVV-A0003
}

func TestProcessTestsFailure(_ *testing.T) {
	if os.Getenv("GO_TEST_PROCESS") != "1" {
		return
	}
	os.Exit(1) // skipcq: RVV-A0003
}

func TestProcessNamedFailures(_ *testing.T) {
	if os.Getenv("GO_TEST_PROCESS") != "1" {
		return
	}
	_, _ = os.Stdout.WriteString(`{"Action":"fail","Package":"example.com/pkg","Test":"TestOne"}` + "\n")
	_, _ = os.Stdout.WriteString(`{"Action":"fail","Package":"example.com/pkg","Test":"TestTwo/subtest"}` + "\n")
	_, _ = os.Stdout.WriteString(`{"Action":"fail","Package":"example.com/pkg"}` + "\n")
	os.Exit(1) // skipcq: RVV-A0003
}

func TestProcessJSONBuildFailure(_ *testing.T) {
	if os.Getenv("GO_TEST_PROCESS") != "1" {
		return
	}
	_, _ = os.Stdout.WriteString(
		`{"Action":"fail","Package":"example.com/pkg","FailedBuild":"example.com/pkg.test"}` + "\n",
	)
	os.Exit(1) // skipcq: RVV-A0003
}

func TestProcessBuildFailure(_ *testing.T) {
	if os.Getenv("GO_TEST_PROCESS") != "1" {
		return
	}
	os.Exit(2) // skipcq: RVV-A0003
}

func TestExecutorRecognizesJSONBuildFailure(t *testing.T) {
	viperSet(map[string]any{
		configuration.UnleashDisableBailKey: true,
		configuration.UnleashDryRunKey:      false,
		configuration.UnleashOutputKey:      "findings.json",
	})
	defer viperReset()

	wdDealer := newWdDealerStub(t)
	mod := gomodule.GoModule{Name: "example.com", Root: ".", CallingDir: "."}
	dealer := engine.NewExecutorDealer(mod, wdDealer, expectedTimeout,
		engine.WithExecContext(fakeExecCommandJSONBuildFailure),
	)
	mut := &mutantStub{
		status:  mutator.Runnable,
		mutType: mutator.ConditionalsBoundary,
		pkg:     "example.com/pkg",
		position: token.Position{
			Filename: "example.go",
			Line:     12,
			Column:   4,
		},
	}
	outCh := make(chan mutator.Mutator, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	dealer.NewExecutor(mut, outCh, &wg).Start(&workerpool.Worker{Name: "test", ID: 1})
	wg.Wait()

	if got := (<-outCh).Status(); got != mutator.NotViable {
		t.Fatalf("mutation status = %s, want %s", got, mutator.NotViable)
	}
}

func TestExecutorReportsEveryNamedKiller(t *testing.T) {
	viperSet(map[string]any{
		configuration.UnleashDisableBailKey: true,
		configuration.UnleashDryRunKey:      false,
		configuration.UnleashOutputKey:      "findings.json",
	})
	defer viperReset()

	wdDealer := newWdDealerStub(t)
	mod := gomodule.GoModule{Name: "example.com", Root: ".", CallingDir: "."}
	dealer := engine.NewExecutorDealer(mod, wdDealer, expectedTimeout,
		engine.WithExecContext(fakeExecCommandNamedFailures),
	)
	mut := &mutantStub{
		status:  mutator.Runnable,
		mutType: mutator.ConditionalsBoundary,
		pkg:     "example.com/pkg",
		position: token.Position{
			Filename: "example.go",
			Line:     12,
			Column:   4,
		},
	}
	outCh := make(chan mutator.Mutator, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	dealer.NewExecutor(mut, outCh, &wg).Start(&workerpool.Worker{Name: "test", ID: 1})
	wg.Wait()

	gotMutant := <-outCh
	if gotMutant.Status() != mutator.Killed {
		t.Fatalf("expected mutation to be killed, got %s", gotMutant.Status())
	}
	want := map[string][]string{
		"example.go:12:4:CONDITIONALS_BOUNDARY": {
			"go:example.com/pkg:TestOne",
			"go:example.com/pkg:TestTwo",
		},
	}
	attribution := dealer.Attribution()
	if diff := cmp.Diff(want, attribution.KilledBy); diff != "" {
		t.Errorf("killer attribution mismatch (-want +got):\n%s", diff)
	}
	id := "example.go:12:4:CONDITIONALS_BOUNDARY"
	if diff := cmp.Diff(map[string]int{id: 0}, attribution.TestsCompleted); diff != "" {
		t.Errorf("completed-test counts mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]bool{id: false}, attribution.AttributionCompleted); diff != "" {
		t.Errorf("completion state mismatch (-want +got):\n%s", diff)
	}
}

func TestMutatorRunInTheCorrectFolder(t *testing.T) {
	t.Run("mutation should run in the correct folder", func(t *testing.T) {
		callingDir := "test/dir"
		mod := gomodule.GoModule{
			Name:       "example.com",
			Root:       ".",
			CallingDir: callingDir,
		}
		wdDealer := newWdDealerStub(t)
		holder := &commandHolder{}
		mjd := engine.NewExecutorDealer(mod, wdDealer, expectedTimeout,
			engine.WithExecContext(fakeExecCommandSuccessWithHolder(holder)))
		mut := &mutantStub{
			status:  mutator.Runnable,
			mutType: mutator.ConditionalsBoundary,
			pkg:     "example.com/my/package",
		}
		outCh := make(chan mutator.Mutator)
		wg := sync.WaitGroup{}
		wg.Add(1)
		executor := mjd.NewExecutor(mut, outCh, &wg)
		w := &workerpool.Worker{
			Name: "test",
			ID:   1,
		}
		go func() {
			<-outCh
			close(outCh)
		}()
		executor.Start(w)
		wg.Wait()

		if mut.Workdir() != holder.cmd.Dir {
			t.Errorf("expected working dir to be %s, got %s", holder.cmd.Dir, mut.Workdir())
		}
	})
	t.Run("integration mode: mutation should run in the wd root folder", func(t *testing.T) {
		viperSet(map[string]any{
			configuration.UnleashIntegrationMode: true,
		})
		defer viperReset()
		callingDir := "test/dir"
		mod := gomodule.GoModule{
			Name:       "example.com",
			Root:       ".",
			CallingDir: callingDir,
		}
		wantRootDir := "/tmp/static"
		wdDealer := &dealerStub{
			t: nil,
			fnGet: func(_ string) (string, error) {
				return wantRootDir, nil
			},
		}

		holder := &commandHolder{}
		mjd := engine.NewExecutorDealer(mod, wdDealer, expectedTimeout,
			engine.WithExecContext(fakeExecCommandSuccessWithHolder(holder)))
		mut := &mutantStub{
			status:  mutator.Runnable,
			mutType: mutator.ConditionalsBoundary,
			pkg:     "example.com/my/package",
		}
		outCh := make(chan mutator.Mutator)
		wg := sync.WaitGroup{}
		wg.Add(1)
		executor := mjd.NewExecutor(mut, outCh, &wg)
		w := &workerpool.Worker{
			Name: "test",
			ID:   1,
		}
		go func() {
			<-outCh
			close(outCh)
		}()
		executor.Start(w)
		wg.Wait()

		if wantRootDir != holder.cmd.Dir {
			t.Errorf("expected working dir to be %q, got %q", wantRootDir, holder.cmd.Dir)
		}
	})
}

func fakeExecCommandSuccess(ctx context.Context, command string, args ...string) *exec.Cmd {
	cs := []string{"-test.run=TestCoverageProcessSuccess", "--", command}
	cs = append(cs, args...)
	// #nosec G204 G702 - We are in tests, we don't care
	cmd := exec.CommandContext(ctx, os.Args[0], cs...)
	cmd.Env = []string{"GO_TEST_PROCESS=1"}

	return cmd
}

func fakeExecCommandSuccessWithHolder(got *commandHolder) execContext {
	return func(ctx context.Context, command string, args ...string) *exec.Cmd {
		dl, _ := ctx.Deadline()
		got.m.Lock()
		defer got.m.Unlock()

		cs := []string{"-test.run=TestCoverageProcessSuccess", "--", command}
		cs = append(cs, args...)
		cmd := getCmd(ctx, cs)

		got.cmd = cmd
		got.command = command
		got.args = args
		got.timeout = time.Until(dl)

		return cmd
	}
}

func fakeExecCommandWithHolder(got *commandHolder, fakeCmd func(ctx context.Context, command string, args ...string) *exec.Cmd) execContext {
	return func(ctx context.Context, command string, args ...string) *exec.Cmd {
		dl, _ := ctx.Deadline()
		got.m.Lock()
		defer got.m.Unlock()

		cmd := fakeCmd(ctx, command, args...)
		got.cmd = cmd
		got.command = command
		got.args = args
		got.timeout = time.Until(dl)

		return cmd
	}
}

func fakeExecCommandTestsFailure(ctx context.Context, command string, args ...string) *exec.Cmd {
	cs := []string{"-test.run=TestProcessTestsFailure", "--", command}
	cs = append(cs, args...)

	return getCmd(ctx, cs)
}

func fakeExecCommandNamedFailures(ctx context.Context, command string, args ...string) *exec.Cmd {
	cs := []string{"-test.run=TestProcessNamedFailures", "--", command}
	cs = append(cs, args...)

	return getCmd(ctx, cs)
}

func fakeExecCommandJSONBuildFailure(ctx context.Context, command string, args ...string) *exec.Cmd {
	cs := []string{"-test.run=TestProcessJSONBuildFailure", "--", command}
	cs = append(cs, args...)

	return getCmd(ctx, cs)
}

func fakeExecCommandBuildFailure(ctx context.Context, command string, args ...string) *exec.Cmd {
	cs := []string{"-test.run=TestProcessBuildFailure", "--", command}
	cs = append(cs, args...)

	return getCmd(ctx, cs)
}

func getCmd(ctx context.Context, cs []string) *exec.Cmd {
	// #nosec G204 G702 - We are in tests, we don't care
	cmd := exec.CommandContext(ctx, os.Args[0], cs...)
	cmd.Env = []string{"GO_TEST_PROCESS=1"}

	return cmd
}
