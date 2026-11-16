package guesthvf

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SupervisorJob describes an INACTIVE, transient user-launchd job proposal.
// Generating it neither registers a service nor qualifies child containment.
// The supervisor executable/entrypoint and a launchd acceptance test are still
// absent. Do not load the result or advertise runtime support on this basis.
type SupervisorJob struct {
	label      string
	executable string
}

func ProposeSupervisor(executable, generation string) (SupervisorJob, error) {
	if !uuid.MatchString(generation) || generation == "00000000-0000-0000-0000-000000000000" || !filepath.IsAbs(executable) || filepath.Clean(executable) != executable || !utf8.ValidString(executable) || strings.IndexFunc(executable, func(r rune) bool { return unicode.IsControl(r) || r == 0xfffe || r == 0xffff }) >= 0 {
		return SupervisorJob{}, ErrPlan
	}
	return SupervisorJob{"local.tenkacloud.battle." + generation, executable}, nil
}

func (j SupervisorJob) Label() string { return j.label }

// Plist returns a proposed registration, not an installed or runnable service.
// No KeepAlive restart or RunAtLoad launch is requested. launchd's documented
// group cleanup covers only children remaining in the job's process group.
// An empty EnvironmentVariables dictionary DOES NOT clear inherited launchd
// environment; the future trusted supervisor must construct its own empty
// QEMU environment. No environment dictionary is emitted to imply otherwise.
func (j SupervisorJob) Plist() ([]byte, error) {
	if j.label == "" || j.executable == "" {
		return nil, ErrPlan
	}
	var executable bytes.Buffer
	if err := xml.EscapeText(&executable, []byte(j.executable)); err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string></array>
<key>RunAtLoad</key><false/>
<key>KeepAlive</key><false/>
<key>AbandonProcessGroup</key><false/>
<key>ExitTimeOut</key><integer>2</integer>
</dict></plist>
`, j.label, executable.String())), nil
}
