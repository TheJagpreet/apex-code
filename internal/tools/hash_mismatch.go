package tools

import (
	"fmt"
	"os"
)

func renderHashMismatch(path, expected string) (Result, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{
				Payload: joinLines([]string{
					"hash_mismatch",
					"current_state: missing",
					"expected_sha256: " + expected,
				}),
				Summary: "hash mismatch; file is missing",
				IsError: true,
			}, fmt.Errorf("file hash mismatch: file missing")
		}
		return Result{}, err
	}
	if info.IsDir() {
		return Result{}, fmt.Errorf("path is a directory: %s", path)
	}
	current, err := fileSHA256(path)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Payload: joinLines([]string{
			"hash_mismatch",
			"expected_sha256: " + expected,
			"current_sha256: " + current,
		}),
		Summary: "hash mismatch; refresh the file and retry",
		IsError: true,
	}, fmt.Errorf("file hash mismatch")
}
