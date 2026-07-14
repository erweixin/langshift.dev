package statemachine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"
)

type contractFile struct {
	Machines map[string]struct {
		Initial     json.RawMessage     `json:"initial"`
		Terminal    []string            `json:"terminal"`
		States      []string            `json:"states"`
		Transitions map[string][]string `json:"transitions"`
	} `json:"machines"`
}

func TestProductionMachinesExactlyMatchFrozenContract(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "contracts", "state-machines", "state-machines.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract contractFile
	if err = json.Unmarshal(encoded, &contract); err != nil {
		t.Fatal(err)
	}
	for name, actual := range AllSnapshots() {
		expected, exists := contract.Machines[name]
		if !exists {
			t.Fatalf("machine %q is missing from frozen contract", name)
		}
		var initial []string
		if len(expected.Initial) > 0 && expected.Initial[0] == '"' {
			var single string
			if err = json.Unmarshal(expected.Initial, &single); err != nil {
				t.Fatal(err)
			}
			initial = []string{single}
		} else if err = json.Unmarshal(expected.Initial, &initial); err != nil {
			t.Fatal(err)
		}
		sort.Strings(initial)
		sort.Strings(expected.Terminal)
		sort.Strings(expected.States)
		for from := range expected.Transitions {
			sort.Strings(expected.Transitions[from])
		}
		if !reflect.DeepEqual(actual.Initial, initial) || !reflect.DeepEqual(actual.Terminal, expected.Terminal) || !reflect.DeepEqual(actual.States, expected.States) || !reflect.DeepEqual(actual.Transitions, expected.Transitions) {
			t.Fatalf("machine %q differs from contracts/state-machines/state-machines.json\nactual=%#v\nexpected initial=%v terminal=%v states=%v transitions=%v", name, actual, initial, expected.Terminal, expected.States, expected.Transitions)
		}
	}
}
