package config

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Size is a number of bytes. In YAML it can be a plain number or have a
// unit, like 512KiB, 500MiB or 2GiB.
type Size int64

var units = []struct {
	suffix string
	mult   int64
}{
	{"GiB", 1 << 30},
	{"MiB", 1 << 20},
	{"KiB", 1 << 10},
	{"B", 1},
}

// ParseSize turns text like "500MiB" into a number of bytes.
func ParseSize(s string) (Size, error) {
	num := strings.TrimSpace(s)
	mult := int64(1)
	for _, u := range units {
		if strings.HasSuffix(num, u.suffix) {
			num = strings.TrimSpace(strings.TrimSuffix(num, u.suffix))
			mult = u.mult
			break
		}
	}

	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n < 0 || n > (1<<62)/mult {
		return 0, fmt.Errorf("invalid size %q (use e.g. 500MiB)", s)
	}
	return Size(n * mult), nil
}

// UnmarshalYAML lets Size fields be written as text like "500MiB" in YAML.
func (s *Size) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseSize(n.Value)
	if err != nil {
		return err
	}
	*s = v
	return nil
}
