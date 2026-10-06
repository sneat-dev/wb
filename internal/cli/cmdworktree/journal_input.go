package cmdworktree

import (
	"bytes"
	"fmt"
	"os"
	"strings"
)

func readPromptBody(prompt, promptFile string) ([]byte, error) {
	if (prompt == "") == (promptFile == "") {
		return nil, fmt.Errorf("supply exactly one of --prompt or --prompt-file")
	}
	if promptFile != "" {
		content, err := os.ReadFile(promptFile)
		if err != nil {
			return nil, fmt.Errorf("read prompt file: %w", err)
		}
		if len(bytes.TrimSpace(content)) == 0 {
			return nil, fmt.Errorf("prompt file %s is empty", promptFile)
		}
		return content, nil
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("--prompt must not be empty")
	}
	return []byte(prompt), nil
}

func journalPath(args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	return "."
}
