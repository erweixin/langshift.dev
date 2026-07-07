package testsupport

import "fmt"

type SequenceIDs struct {
	values []string
	next   int
}

func NewSequenceIDs(values ...string) *SequenceIDs {
	return &SequenceIDs{values: values}
}

func (g *SequenceIDs) NewID() (string, error) {
	if g.next >= len(g.values) {
		id := fmt.Sprintf("generated_%d", g.next)
		g.next++
		return id, nil
	}
	id := g.values[g.next]
	g.next++
	return id, nil
}
