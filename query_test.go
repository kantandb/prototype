package main

import (
	"testing"

	"github.com/theory/jsonpath"
)

func TestJSONPathPointer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path    string
		pointer string
		ok      bool
	}{
		{path: `$.profile.age`, pointer: "/profile/age", ok: true},
		{path: `$['a/b']['~x'][0]`, pointer: "/a~1b/~0x/0", ok: true},
		{path: `$`, ok: false},
		{path: `$.items[-1]`, ok: false},
		{path: `$.items[*]`, ok: false},
		{path: `$..name`, ok: false},
		{path: `$['a','b']`, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			path, err := jsonpath.Parse(tt.path)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			pointer, ok := jsonPathPointer(path)
			if pointer != tt.pointer || ok != tt.ok {
				t.Errorf("jsonPathPointer() = (%q, %t), want (%q, %t)", pointer, ok, tt.pointer, tt.ok)
			}
		})
	}
}

func TestPlanPathQuery(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	if err := store.createDB("scores", indexDef{name: "first", path: "/score"}, indexDef{name: "second", path: "/score"}); err != nil {
		t.Fatalf("createDB() error = %v", err)
	}

	plan, err := store.planPathQuery("scores", `$.score`)
	if err != nil {
		t.Fatalf("planPathQuery() error = %v", err)
	}
	if plan.text != `$["score"]` || plan.index != "first" {
		t.Errorf("plan = %+v, want canonical path and first matching index", plan)
	}

	plan, err = store.planPathQuery("scores", `$..score`)
	if err != nil {
		t.Fatalf("planPathQuery() error = %v", err)
	}
	if plan.index != "" {
		t.Errorf("descendant index = %q, want scan", plan.index)
	}
}
