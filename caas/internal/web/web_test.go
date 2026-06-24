package web

import "testing"

func nodes3() *Server {
	return &Server{NodeTemplates: []NodeTemplate{
		{Node: "lab01", TemplateID: 106},
		{Node: "lab02", TemplateID: 107},
		{Node: "lab03", TemplateID: 108},
	}}
}

func TestSpreadWorkers(t *testing.T) {
	s := nodes3()
	sz := resolveSize(defaultSize)
	cases := map[int][]int32{
		4: {2, 1, 1}, // remainder to first nodes
		3: {1, 1, 1},
		1: {1, 0, 0}, // scale-down: pools persist with 0 so the MD isn't orphaned
		0: {0, 0, 0},
	}
	for count, want := range cases {
		got := s.spreadWorkers(count, sz)
		if len(got) != len(s.NodeTemplates) {
			t.Fatalf("count=%d: got %d pools, want one per node (%d)", count, len(got), len(s.NodeTemplates))
		}
		for i, w := range got {
			if w.Replicas != want[i] {
				t.Errorf("count=%d pool[%s].Replicas = %d, want %d", count, w.Name, w.Replicas, want[i])
			}
			if w.SourceNode != s.NodeTemplates[i].Node || w.TemplateID != s.NodeTemplates[i].TemplateID {
				t.Errorf("count=%d pool[%d] node/template mismatch: %+v", count, i, w)
			}
		}
	}
}

func TestSpreadWorkersNoNodes(t *testing.T) {
	s := &Server{}
	if got := s.spreadWorkers(3, resolveSize(defaultSize)); got != nil {
		t.Errorf("expected nil with no node templates, got %+v", got)
	}
}
