package llm

import (
	"crypto/rand"
	"io"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

var defaultIDEntropy = rand.Reader

type IDGenerator interface {
	NewID() (string, error)
}

type ULIDGenerator struct {
	mu      sync.Mutex
	entropy io.Reader
}

func NewULIDGenerator(entropy io.Reader) *ULIDGenerator {
	if entropy == nil {
		entropy = defaultIDEntropy
	}
	return &ULIDGenerator{
		entropy: ulid.Monotonic(entropy, 0),
	}
}

func (g *ULIDGenerator) NewID() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	id, err := ulid.New(ulid.Timestamp(time.Now().UTC()), g.entropy)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}
