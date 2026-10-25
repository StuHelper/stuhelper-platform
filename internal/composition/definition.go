// Package composition loads the checked module specification used by the binary.
package composition

import (
	_ "embed"
	"encoding/json"

	"github.com/StuHelper/stuhelper-platform/module"
)

//go:embed platform.json
var platformSpec []byte

// Platform returns the descriptor generated from the shared engineering package.
func Platform() (module.Definition, error) {
	var spec struct {
		Descriptor module.Descriptor `json:"descriptor"`
		Critical   bool              `json:"critical"`
	}
	if err := json.Unmarshal(platformSpec, &spec); err != nil {
		return module.Definition{}, err
	}
	return module.Definition{Descriptor: spec.Descriptor, Critical: spec.Critical}, nil
}
