package output

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

func Write(w io.Writer, value any, format string) error {
	switch format {
	case "text":
		return writeText(w, value)
	case "json":
		raw, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(raw))
		return err
	case "yaml":
		raw, err := yaml.Marshal(value)
		if err != nil {
			return err
		}
		_, err = w.Write(raw)
		return err
	case "toml":
		return toml.NewEncoder(w).Encode(value)
	default:
		return fmt.Errorf("unsupported output format %q", format)
	}
}

func writeText(w io.Writer, value any) error {
	type textResult interface {
		Text(io.Writer) error
	}
	if v, ok := value.(textResult); ok {
		return v.Text(w)
	}
	_, err := fmt.Fprintf(w, "%+v\n", value)
	return err
}
