//go:build !darwin

package infra

import "fmt"

func procArgsOS(int) (string, []string, map[string]string, error) {
	return "", nil, nil, fmt.Errorf("reading another process's arguments is only implemented on macOS")
}
