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
	"go/token"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/go-gremlins/gremlins/internal/mutator"
)

func TestAttributionStoreSnapshotsAreDetached(t *testing.T) {
	t.Parallel()

	m := &attributionMutator{
		position: token.Position{Filename: "example.go", Line: 12, Column: 4},
	}
	input := []string{"go:example.com:TestOne"}
	store := newAttributionStore()
	store.record(m, input, 2, true)

	input[0] = "changed"
	first := store.snapshot()
	fact := first[mutator.ID(m)]
	fact.killedBy[0] = "also-changed"
	fact.testsCompleted = 0
	fact.complete = false
	first[mutator.ID(m)] = fact
	first["new"] = attributionFacts{killedBy: []string{"value"}}

	want := map[string]attributionFacts{
		"example.go:12:4:CONDITIONALS_BOUNDARY": {
			killedBy:       []string{"go:example.com:TestOne"},
			testsCompleted: 2,
			complete:       true,
		},
	}
	got := store.snapshot()
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(attributionFacts{})); diff != "" {
		t.Errorf("snapshot mismatch (-want +got):\n%s", diff)
	}
}

type attributionMutator struct {
	position token.Position
}

func (*attributionMutator) Type() mutator.Type         { return mutator.ConditionalsBoundary }
func (*attributionMutator) SetType(mutator.Type)       {}
func (*attributionMutator) Status() mutator.Status     { return mutator.Killed }
func (*attributionMutator) SetStatus(mutator.Status)   {}
func (m *attributionMutator) Position() token.Position { return m.position }
func (*attributionMutator) Pos() token.Pos             { return token.NoPos }
func (*attributionMutator) Pkg() string                { return "example.com" }
func (*attributionMutator) SetWorkdir(string)          {}
func (*attributionMutator) Workdir() string            { return "" }
func (*attributionMutator) Apply() error               { return nil }
func (*attributionMutator) Rollback() error            { return nil }
func (*attributionMutator) OrigSnippet() []byte        { return nil }
func (*attributionMutator) MutatedSnippet() []byte     { return nil }
