package scheduler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"time"
)

var ErrInvalidStateEncoding = errors.New("scheduler state encoding is invalid")

type stateDocument struct {
	Version  int               `json:"version"`
	Deficits []deficitDocument `json:"deficits"`
	Buckets  []bucketDocument  `json:"buckets"`
	Cursors  []cursorDocument  `json:"cursors"`
}

type deficitDocument struct {
	Tenant   string `json:"tenant"`
	Resource string `json:"resource"`
	Units    int64  `json:"units"`
}

type bucketDocument struct {
	Tenant     string    `json:"tenant"`
	Resource   string    `json:"resource"`
	Tokens     int64     `json:"tokens"`
	LastRefill time.Time `json:"last_refill"`
	Remainder  int64     `json:"remainder"`
}

type cursorDocument struct {
	Resource string `json:"resource"`
	Tenant   string `json:"tenant"`
}

func EncodeState(config Config, state State) ([]byte, error) {
	if !validConfig(config) || !validState(config, state) {
		return nil, ErrInvalidStateEncoding
	}
	document := stateDocument{Version: 1, Deficits: make([]deficitDocument, 0, len(state.Deficit)), Buckets: make([]bucketDocument, 0, len(state.Buckets)), Cursors: make([]cursorDocument, 0, len(state.CursorByResource))}
	for key, units := range state.Deficit {
		document.Deficits = append(document.Deficits, deficitDocument{Tenant: key.Tenant, Resource: key.Resource, Units: units})
	}
	for key, bucket := range state.Buckets {
		document.Buckets = append(document.Buckets, bucketDocument{Tenant: key.Tenant, Resource: key.Resource, Tokens: bucket.Tokens, LastRefill: bucket.LastRefill.UTC(), Remainder: bucket.RefillRemainder})
	}
	for resource, tenant := range state.CursorByResource {
		document.Cursors = append(document.Cursors, cursorDocument{Resource: resource, Tenant: tenant})
	}
	sort.Slice(document.Deficits, func(left, right int) bool {
		return stateKeyLess(document.Deficits[left].Resource, document.Deficits[left].Tenant, document.Deficits[right].Resource, document.Deficits[right].Tenant)
	})
	sort.Slice(document.Buckets, func(left, right int) bool {
		return stateKeyLess(document.Buckets[left].Resource, document.Buckets[left].Tenant, document.Buckets[right].Resource, document.Buckets[right].Tenant)
	})
	sort.Slice(document.Cursors, func(left, right int) bool {
		return stateKeyLess(document.Cursors[left].Resource, document.Cursors[left].Tenant, document.Cursors[right].Resource, document.Cursors[right].Tenant)
	})
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, ErrInvalidStateEncoding
	}
	return encoded, nil
}

func DecodeState(config Config, encoded []byte) (State, error) {
	if !validConfig(config) || len(encoded) == 0 || len(encoded) > 1<<20 {
		return State{}, ErrInvalidStateEncoding
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var document stateDocument
	if err := decoder.Decode(&document); err != nil || document.Version != 1 {
		return State{}, ErrInvalidStateEncoding
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return State{}, ErrInvalidStateEncoding
	}
	state := State{Deficit: map[TenantResource]int64{}, Buckets: map[TenantResource]BucketState{}, CursorByResource: map[string]string{}}
	for _, entry := range document.Deficits {
		key := TenantResource{Tenant: entry.Tenant, Resource: entry.Resource}
		if _, duplicate := state.Deficit[key]; duplicate {
			return State{}, ErrInvalidStateEncoding
		}
		state.Deficit[key] = entry.Units
	}
	for _, entry := range document.Buckets {
		key := TenantResource{Tenant: entry.Tenant, Resource: entry.Resource}
		if _, duplicate := state.Buckets[key]; duplicate {
			return State{}, ErrInvalidStateEncoding
		}
		state.Buckets[key] = BucketState{Tokens: entry.Tokens, LastRefill: entry.LastRefill.UTC(), RefillRemainder: entry.Remainder}
	}
	for _, entry := range document.Cursors {
		if _, duplicate := state.CursorByResource[entry.Resource]; duplicate {
			return State{}, ErrInvalidStateEncoding
		}
		state.CursorByResource[entry.Resource] = entry.Tenant
	}
	if !validState(config, state) {
		return State{}, ErrInvalidStateEncoding
	}
	return state, nil
}

func stateKeyLess(leftResource, leftTenant, rightResource, rightTenant string) bool {
	return leftResource < rightResource || leftResource == rightResource && leftTenant < rightTenant
}
