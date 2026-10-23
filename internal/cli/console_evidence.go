package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
	"github.com/wangyiliangnet/android-publisher-cli/internal/strictjson"
)

func runConsoleEvidence(mode, root string, args []string, out, stderr io.Writer) int {
	bad := func(err error) int { return failure(out, mode, "verify_console_evidence", 2, err) }
	if len(args) == 0 {
		return bad(fmt.Errorf("evidence requires template or import"))
	}
	f := flags("evidence "+args[0], stderr)
	file := f.String("file", "", "private console evidence JSON")
	id := f.String("release", "", "frozen release ID")
	channel := f.String("channel", "", "channel ID")
	if err := parse(f, args[1:]); err != nil {
		return bad(err)
	}
	if *file == "" || (args[0] != "template" && args[0] != "import") {
		return bad(fmt.Errorf("invalid evidence arguments"))
	}
	if mode == manifest.Production && filepath.Clean(root) != defaultRoot(mode) {
		return bad(fmt.Errorf("production evidence requires the canonical directory"))
	}
	s, err := state.Open(root, mode, args[0] == "template")
	if err != nil {
		return productionFailure(out, err)
	}
	defer s.Close()
	if args[0] == "template" {
		e, err := s.ConsoleTemplate(*id, *channel)
		if err != nil {
			return bad(err)
		}
		data, _ := json.MarshalIndent(e, "", "  ")
		if err = writeSame(*file, data); err != nil {
			return bad(err)
		}
		return encodeCode(out, map[string]any{"schema_version": 1, "effect": "read_only", "evidence_template": *file, "next_action": "fill_observed_fields_from_console"})
	}
	if *id != "" || *channel != "" {
		return bad(fmt.Errorf("evidence import accepts only --file"))
	}
	data, err := privatefs.Read(*file, 65536)
	var e state.ConsoleEvidence
	if err != nil || strictjson.Decode(data, &e) != nil {
		return bad(fmt.Errorf("invalid console evidence JSON"))
	}
	if err = s.ImportConsoleEvidence(e); err != nil {
		return bad(err)
	}
	return encodeCode(out, map[string]any{"schema_version": 1, "release_id": e.ReleaseID, "channel": e.Expected.Channel, "effect": "local_observation_only", "source": "manual_console", "next_action": "status"})
}
