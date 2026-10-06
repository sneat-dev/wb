package shared

import (
	"fmt"
	"io"
)

func WriteLine(writer io.Writer, values ...any) error {
	_, err := fmt.Fprintln(writer, values...)
	return err
}
func WriteFormat(writer io.Writer, format string, values ...any) error {
	_, err := fmt.Fprintf(writer, format, values...)
	return err
}
