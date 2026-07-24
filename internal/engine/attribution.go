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
	"bytes"
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/go-gremlins/gremlins/internal/mutator"
)

type attributionStore struct {
	facts map[string]attributionFacts
	mutex sync.RWMutex
}

type attributionFacts struct {
	killedBy       []string
	testsCompleted int
	complete       bool
}

// Attribution contains the test inventory and per-mutant execution evidence.
type Attribution struct {
	KilledBy             map[string][]string
	TestsCompleted       map[string]int
	AttributionCompleted map[string]bool
	Tests                []string
}

func newAttributionStore() *attributionStore {
	return &attributionStore{
		facts: make(map[string]attributionFacts),
	}
}

func (s *attributionStore) record(
	m mutator.Mutator,
	killedBy []string,
	testsCompleted int,
	complete bool,
) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.facts[mutator.ID(m)] = attributionFacts{
		killedBy:       append([]string(nil), killedBy...),
		testsCompleted: testsCompleted,
		complete:       complete,
	}
}

func (s *attributionStore) snapshot() map[string]attributionFacts {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	facts := maps.Clone(s.facts)
	for id, fact := range facts {
		fact.killedBy = append([]string(nil), fact.killedBy...)
		facts[id] = fact
	}

	return facts
}

func buildAttribution(
	inventory map[string]struct{},
	facts map[string]attributionFacts,
) Attribution {
	result := Attribution{
		KilledBy:             make(map[string][]string, len(facts)),
		TestsCompleted:       make(map[string]int, len(facts)),
		AttributionCompleted: make(map[string]bool, len(facts)),
		Tests:                make([]string, 0, len(inventory)),
	}
	for id, fact := range facts {
		result.KilledBy[id] = fact.killedBy
		result.TestsCompleted[id] = fact.testsCompleted
		result.AttributionCompleted[id] = fact.complete
	}
	for test := range inventory {
		result.Tests = append(result.Tests, test)
	}
	slices.Sort(result.Tests)

	return result
}

type goTestEvent struct {
	// The go test -json protocol uses capitalized field names.
	//nolint:tagliatelle
	Action string `json:"Action"`
	//nolint:tagliatelle
	Package string `json:"Package"`
	//nolint:tagliatelle
	Test string `json:"Test"`
	//nolint:tagliatelle
	Output string `json:"Output"`
	//nolint:tagliatelle
	FailedBuild string `json:"FailedBuild"`
}

type goTestRun struct {
	failed      []string
	completed   map[string]struct{}
	failedBuild bool
}

var goTestName = regexp.MustCompile(`^(?:Test|Example|Fuzz)\S*$`)

func parseGoTestInventory(output []byte) map[string]struct{} {
	inventory := make(map[string]struct{})
	forEachGoTestEvent(output, func(event goTestEvent) {
		name := strings.TrimSpace(event.Output)
		if event.Action == "output" && event.Test == "" && goTestName.MatchString(name) {
			inventory[testID(event.Package, name)] = struct{}{}
		}
	})

	return inventory
}

func parseGoTestRun(output []byte) goTestRun {
	failed := make(map[string]struct{})
	completed := make(map[string]struct{})
	failedBuild := false
	forEachGoTestEvent(output, func(event goTestEvent) {
		if event.FailedBuild != "" {
			failedBuild = true
		}
		if event.Test == "" {
			return
		}
		topLevel := strings.SplitN(event.Test, "/", 2)[0]
		if event.Action == "fail" {
			failed[testID(event.Package, topLevel)] = struct{}{}
		}
		if event.Action == "pass" || event.Action == "fail" || event.Action == "skip" {
			completed[testID(event.Package, topLevel)] = struct{}{}
		}
	})

	failedTests := make([]string, 0, len(failed))
	for test := range failed {
		failedTests = append(failedTests, test)
	}
	slices.Sort(failedTests)

	return goTestRun{
		failed:      failedTests,
		completed:   completed,
		failedBuild: failedBuild,
	}
}

func forEachGoTestEvent(output []byte, visit func(goTestEvent)) {
	for _, line := range bytes.Split(output, []byte("\n")) {
		var event goTestEvent
		if json.Unmarshal(line, &event) == nil {
			visit(event)
		}
	}
}

func testRunProgress(
	inventory map[string]struct{},
	completed map[string]struct{},
	pkg string,
	integrationMode bool,
) (int, bool) {
	prefix := "go:" + pkg + ":"
	selectedTests := 0
	testsCompleted := 0
	for test := range inventory {
		if !integrationMode && !strings.HasPrefix(test, prefix) {
			continue
		}
		selectedTests++
		if _, ok := completed[test]; !ok {
			continue
		}
		testsCompleted++
	}

	if selectedTests == 0 {
		return 0, false
	}

	return testsCompleted, testsCompleted == selectedTests
}

func testID(pkg, test string) string {
	return "go:" + pkg + ":" + test
}
