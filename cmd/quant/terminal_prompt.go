package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func isInteractiveInput() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func prompt(reader *bufio.Reader, label string) (string, error) {
	if _, err := fmt.Fprint(os.Stdout, label); err != nil {
		return "", err
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
