package cli

import (
	"errors"
	"fmt"

	"hal9001/internal/authcfg"
	"hal9001/internal/spec"
)

// writePasswordHash stores hash as the `hash` option of every `password`
// module in config.yaml's auth stacks and returns how many entries it set
// (authcfg.SetPassword: comments and layout survive, and the password module is
// added to the stacks when none has it). The write goes through
// spec.WriteConfig and fails on a concurrent edit.
func writePasswordHash(home, hash string) (int, error) {
	src, onDisk, err := spec.ReadFile(home, spec.KindConfig, "")
	if err != nil && !errors.Is(err, spec.ErrNotFound) {
		return 0, err
	}
	out, n, err := authcfg.SetPassword(src, hash)
	if err != nil {
		return 0, err
	}
	if _, ds := spec.CheckConfig(out); spec.HasErrors(ds) {
		return 0, fmt.Errorf("config.yaml would be invalid after the change: %s", ds[0])
	}
	if _, err := spec.WriteConfig(home, out, onDisk); err != nil {
		return 0, err
	}
	return n, nil
}
