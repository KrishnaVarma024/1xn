// Package ids makes and checks identifiers.
//
// Every id is a UUIDv7: 48 bits of millisecond timestamp followed by random
// bits. Devices create them offline with no coordination, they never collide
// in practice, and because they start with the time they sort by creation
// order, which keeps B-tree indexes compact (random UUIDv4 keys scatter
// inserts across the whole index).
package ids

import (
	"fmt"

	"github.com/google/uuid"
)

// New returns a fresh UUIDv7.
func New() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}

// ParseV7 parses s and insists it is a version 7 UUID. Ids that come from
// devices go through here, so a buggy client sending v4 ids is caught at the
// door.
func ParseV7(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, fmt.Errorf("not a UUID: %q", s)
	}
	if id.Version() != 7 {
		return uuid.Nil, fmt.Errorf("UUID %s is version %d, want 7", id, id.Version())
	}
	return id, nil
}
