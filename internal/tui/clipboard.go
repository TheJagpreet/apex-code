package tui

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	osc52 "github.com/aymanbagabas/go-osc52/v2"
)

var clipboardCopy = copyToClipboard

func copyToClipboard(text string) error {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if err := copyNative(text); err == nil {
		return nil
	}
	_, err := fmt.Fprint(os.Stderr, osc52Sequence(text))
	return err
}

func copyNative(text string) error {
	switch runtime.GOOS {
	case "windows":
		return pipeClipboard("cmd", []string{"/c", "clip"}, text)
	case "darwin":
		return pipeClipboard("pbcopy", nil, text)
	default:
		for _, candidate := range []struct {
			name string
			args []string
		}{
			{name: "wl-copy"},
			{name: "xclip", args: []string{"-selection", "clipboard"}},
			{name: "xsel", args: []string{"--clipboard", "--input"}},
		} {
			if err := pipeClipboard(candidate.name, candidate.args, text); err == nil {
				return nil
			}
		}
		return fmt.Errorf("no native clipboard command available")
	}
}

func pipeClipboard(name string, args []string, text string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("%s: %w", msg, err)
		}
		return err
	}
	return nil
}

func osc52Sequence(text string) string {
	seq := osc52.New(text)
	switch {
	case os.Getenv("TMUX") != "":
		return seq.Tmux().String()
	case os.Getenv("STY") != "":
		return seq.Screen().String()
	default:
		return seq.String()
	}
}
