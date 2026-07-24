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

package cmd

import (
	"context"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/go-gremlins/gremlins/internal/configuration"
	"github.com/go-gremlins/gremlins/internal/gomodule"
	"github.com/go-gremlins/gremlins/internal/mutator"
	"github.com/go-gremlins/gremlins/internal/report"
)

func TestUnleash(t *testing.T) {
	const falseDefault = "false"

	c, err := newUnleashCmd(context.Background())
	if err != nil {
		t.Fatal("newUnleashCmd should no fail")
	}
	cmd := c.cmd

	if cmd.Name() != "unleash" {
		t.Errorf("expected 'unleash', got %q", cmd.Name())
	}

	flags := cmd.Flags()

	testCases := []struct {
		name      string
		shorthand string
		flagType  string
		defValue  string
	}{
		{
			name:     "arithmetic-base",
			flagType: "bool",
			defValue: "true",
		},
		{
			name:     "conditionals-boundary",
			flagType: "bool",
			defValue: "true",
		},
		{
			name:     "conditionals_negation",
			flagType: "bool",
			defValue: "true",
		},
		{
			name:     "coverpkg",
			flagType: "string",
			defValue: "",
		},
		{
			name:     "disable-bail",
			flagType: "bool",
			defValue: falseDefault,
		},
		{
			name:      "diff",
			shorthand: "D",
			flagType:  "string",
			defValue:  "",
		},
		{
			name:      "dry-run",
			shorthand: "d",
			flagType:  "bool",
			defValue:  falseDefault,
		},
		{
			name:     "increment-decrement",
			flagType: "bool",
			defValue: "true",
		},
		{
			name:      "integration",
			shorthand: "i",
			flagType:  "bool",
			defValue:  falseDefault,
		},
		{
			name:     "invert-assignments",
			flagType: "bool",
			defValue: falseDefault,
		},
		{
			name:     "invert-bitwise",
			flagType: "bool",
			defValue: falseDefault,
		},
		{
			name:     "invert-bwassign",
			flagType: "bool",
			defValue: falseDefault,
		},

		{
			name:     "invert-logical",
			flagType: "bool",
			defValue: falseDefault,
		},
		{
			name:     "invert-loopctrl",
			flagType: "bool",
			defValue: falseDefault,
		},
		{
			name:     "invert-negatives",
			flagType: "bool",
			defValue: "true",
		},
		{
			name:      "output",
			shorthand: "o",
			flagType:  "string",
			defValue:  "",
		},
		{
			name:     "remove-self-assignments",
			flagType: "bool",
			defValue: falseDefault,
		},
		{
			name:      "tags",
			shorthand: "t",
			flagType:  "string",
			defValue:  "",
		},
		{
			name:     "test-cpu",
			flagType: "int",
			defValue: "0",
		},
		{
			name:     "threshold-efficacy",
			flagType: "float64",
			defValue: "0",
		},
		{
			name:     "threshold-mcover",
			flagType: "float64",
			defValue: "0",
		},
		{
			name:     "timeout-coefficient",
			flagType: "int",
			defValue: "0",
		},
		{
			name:     "workers",
			flagType: "int",
			defValue: "0",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			f := flags.Lookup(tc.name)
			if f == nil {
				t.Fatalf("expected flag %q to be registered", tc.name)
			}
			if tc.shorthand != "" && f.Shorthand != tc.shorthand {
				t.Errorf("expected %q to have a shorthand %q, got %q", tc.name, tc.shorthand, f.Shorthand)
			}
			if f.Value.Type() != tc.flagType {
				t.Errorf("expected %q to be type %q, got %q", tc.name, f.Value.Type(), f.Value.Type())
			}
			if f.DefValue != tc.defValue {
				t.Errorf("expected %q to have default value %q, got %q", tc.name, tc.defValue, f.DefValue)
			}
		})
	}

	// test for MutantTypes flags
	for _, mt := range mutator.Types {
		s := strings.ToLower(mt.String())
		mtf := flags.Lookup(s)
		if mtf == nil {
			t.Errorf("expected to have flag for mutant type: %s", mt)

			continue
		}

		if mtf.Value.Type() != "bool" {
			t.Errorf("expected %q to be a %q, got %q", s, "bool", mtf.Value.Type())
		}
		wantDef := fmt.Sprintf("%v", configuration.IsDefaultEnabled(mt))
		if mtf.DefValue != wantDef {
			t.Errorf("expected %q have default %q, got %q", s, wantDef, mtf.DefValue)
		}
	}
}

func TestAttributionComplete(t *testing.T) {
	const (
		testFilename = "example.go"
		outputFile   = "findings.json"
		killerTest   = "go:example.com:TestOne"
	)

	killed := &unleashMutant{
		position: token.Position{Filename: testFilename, Line: 2, Column: 3},
		status:   mutator.Killed,
	}
	lived := &unleashMutant{
		position: token.Position{Filename: testFilename, Line: 4, Column: 5},
		status:   mutator.Lived,
	}
	timedOut := &unleashMutant{
		position: token.Position{Filename: testFilename, Line: 6, Column: 7},
		status:   mutator.TimedOut,
	}
	notViable := &unleashMutant{
		position: token.Position{Filename: testFilename, Line: 8, Column: 9},
		status:   mutator.NotViable,
	}
	runnable := &unleashMutant{
		position: token.Position{Filename: testFilename, Line: 10, Column: 11},
		status:   mutator.Runnable,
	}
	id := mutator.ID(killed)
	livedID := mutator.ID(lived)
	testCases := map[string]struct {
		output  string
		results report.Results
		want    bool
	}{
		"requires_disable_bail": {
			output: outputFile,
			results: report.Results{
				Mutants:              []mutator.Mutator{killed},
				KilledBy:             map[string][]string{id: {killerTest}},
				AttributionCompleted: map[string]bool{id: true},
			},
		},
		"requires_machine_output": {
			results: report.Results{
				DisableBail:          true,
				Mutants:              []mutator.Mutator{killed},
				KilledBy:             map[string][]string{id: {killerTest}},
				AttributionCompleted: map[string]bool{id: true},
			},
		},
		"rejects_dry_run": {
			output: outputFile,
			results: report.Results{
				DisableBail:          true,
				Mutants:              []mutator.Mutator{killed},
				KilledBy:             map[string][]string{id: {killerTest}},
				AttributionCompleted: map[string]bool{id: true},
			},
		},
		"rejects_unnamed_package_failure": {
			output: outputFile,
			results: report.Results{
				DisableBail:          true,
				Mutants:              []mutator.Mutator{killed},
				KilledBy:             map[string][]string{id: nil},
				AttributionCompleted: map[string]bool{id: true},
			},
		},
		"rejects_interrupted_test_run": {
			output: outputFile,
			results: report.Results{
				DisableBail:          true,
				Mutants:              []mutator.Mutator{killed},
				KilledBy:             map[string][]string{id: {killerTest}},
				AttributionCompleted: map[string]bool{id: false},
			},
		},
		"rejects_incomplete_surviving_mutant": {
			output: outputFile,
			results: report.Results{
				DisableBail:          true,
				Mutants:              []mutator.Mutator{lived},
				AttributionCompleted: map[string]bool{livedID: false},
			},
		},
		"rejects_timed_out_mutant": {
			output: outputFile,
			results: report.Results{
				DisableBail: true,
				Mutants:     []mutator.Mutator{timedOut},
			},
		},
		"rejects_unfinished_mutant": {
			output: outputFile,
			results: report.Results{
				DisableBail: true,
				Mutants:     []mutator.Mutator{runnable},
			},
		},
		"accepts_nonviable_mutant_without_test_attribution": {
			output: outputFile,
			results: report.Results{
				DisableBail: true,
				Mutants:     []mutator.Mutator{notViable},
			},
			want: true,
		},
		"accepts_complete_surviving_mutant": {
			output: outputFile,
			results: report.Results{
				DisableBail:          true,
				Mutants:              []mutator.Mutator{lived},
				AttributionCompleted: map[string]bool{livedID: true},
			},
			want: true,
		},
		"accepts_every_named_killer": {
			output: outputFile,
			results: report.Results{
				DisableBail:          true,
				Mutants:              []mutator.Mutator{killed},
				KilledBy:             map[string][]string{id: {killerTest}},
				AttributionCompleted: map[string]bool{id: true},
			},
			want: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			configuration.Set(configuration.UnleashOutputKey, tc.output)
			configuration.Set(configuration.UnleashDryRunKey, name == "rejects_dry_run")
			t.Cleanup(configuration.Reset)

			if got := attributionComplete(tc.results); got != tc.want {
				t.Errorf("attributionComplete() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestRunAttachesAttributionMetadata(t *testing.T) {
	moduleDir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/fixture\n\ngo 1.23\n",
		"fixture.go": `package fixture

func IsPositive(value int) bool {
	return value > 0
}
`,
		"fixture_test.go": `package fixture

import "testing"

func TestPositive(t *testing.T) {
	if !IsPositive(1) {
		t.Fatal("expected a positive number")
	}
}

func TestNegative(t *testing.T) {
	if IsPositive(-1) {
		t.Fatal("expected a negative number")
	}
}

func TestZero(t *testing.T) {
	if IsPositive(0) {
		t.Fatal("expected zero not to be positive")
	}
}
`,
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(moduleDir, name), []byte(contents), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}

	configuration.Reset()
	configuration.Set(configuration.UnleashDisableBailKey, true)
	configuration.Set(configuration.UnleashOutputKey, filepath.Join(t.TempDir(), "findings.json"))
	configuration.Set(configuration.UnleashWorkersKey, 1)
	configuration.Set(configuration.MutantTypeEnabledKey(mutator.ConditionalsBoundary), true)
	configuration.Set(configuration.MutantTypeEnabledKey(mutator.ConditionalsNegation), true)
	t.Cleanup(configuration.Reset)

	mod, err := gomodule.Init(moduleDir)
	if err != nil {
		t.Fatalf("initialize fixture module: %v", err)
	}
	results, err := run(context.Background(), mod, t.TempDir())
	if err != nil {
		t.Fatalf("run fixture: %v", err)
	}
	if results.KilledBy == nil {
		t.Error("expected an initialized killer-attribution result")
	}
	if !results.DisableBail {
		t.Error("expected disable-bail metadata")
	}
	if !results.AttributionComplete {
		t.Error("expected complete attribution")
	}
	wantTests := []string{
		"go:example.com/fixture:TestNegative",
		"go:example.com/fixture:TestPositive",
		"go:example.com/fixture:TestZero",
	}
	if diff := cmp.Diff(wantTests, results.Tests); diff != "" {
		t.Errorf("test inventory mismatch (-want +got):\n%s", diff)
	}
	for id, completed := range results.TestsCompleted {
		if completed != 3 {
			t.Errorf("mutant %s completed %d tests, want 3", id, completed)
		}
	}
	foundMultipleKillers := false
	for _, killedBy := range results.KilledBy {
		if len(killedBy) >= 2 {
			foundMultipleKillers = true

			break
		}
	}
	if !foundMultipleKillers {
		t.Errorf(
			"expected one mutant killed by multiple tests, got attribution %v for %d mutants",
			results.KilledBy,
			len(results.Mutants),
		)
	}
}

type unleashMutant struct {
	position token.Position
	status   mutator.Status
}

func (*unleashMutant) Type() mutator.Type         { return mutator.ConditionalsBoundary }
func (*unleashMutant) SetType(mutator.Type)       {}
func (m *unleashMutant) Status() mutator.Status   { return m.status }
func (*unleashMutant) SetStatus(mutator.Status)   {}
func (m *unleashMutant) Position() token.Position { return m.position }
func (*unleashMutant) Pos() token.Pos             { return token.NoPos }
func (*unleashMutant) Pkg() string                { return "example.com" }
func (*unleashMutant) SetWorkdir(string)          {}
func (*unleashMutant) Workdir() string            { return "" }
func (*unleashMutant) Apply() error               { return nil }
func (*unleashMutant) Rollback() error            { return nil }
func (*unleashMutant) OrigSnippet() []byte        { return nil }
func (*unleashMutant) MutatedSnippet() []byte     { return nil }
