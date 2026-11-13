package guesthvf

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func TestSupervisorProposalDoesNotAutoLaunchRestartOrAbandonGroup(t *testing.T) {
	j, err := ProposeSupervisor("/private/operator/supervisor", generation)
	if err != nil {
		t.Fatal(err)
	}
	b, err := j.Plist()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, key := range []string{"RunAtLoad", "KeepAlive", "AbandonProcessGroup"} {
		if !strings.Contains(s, "<key>"+key+"</key><false/>") {
			t.Fatal(key)
		}
	}
	if !strings.Contains(s, "<key>ExitTimeOut</key><integer>2</integer>") || j.Label() != "local.tenkacloud.battle."+generation {
		t.Fatal(s)
	}
	for _, forbidden := range []string{"EnvironmentVariables", "WatchPaths", "QueueDirectories", "StartInterval", "MachServices", "Sockets", "UserName", "EnablePressuredExit", "<true/>"} {
		if strings.Contains(s, forbidden) {
			t.Fatal(forbidden)
		}
	}
	b[0] = 'x'
	next, _ := j.Plist()
	if next[0] != '<' {
		t.Fatal("aliased proposal")
	}
}

func TestSupervisorProposalEscapesXMLRatherThanInjectingAuthority(t *testing.T) {
	path := "/private/operator/a<&b>"
	j, err := ProposeSupervisor(path, generation)
	if err != nil {
		t.Fatal(err)
	}
	data, err := j.Plist()
	if err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	var stringsSeen []string
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if element, ok := token.(xml.StartElement); ok && element.Name.Local == "string" {
			var value string
			if err := decoder.DecodeElement(&value, &element); err != nil {
				t.Fatal(err)
			}
			stringsSeen = append(stringsSeen, value)
		}
	}
	if len(stringsSeen) != 2 || stringsSeen[1] != path {
		t.Fatal(stringsSeen)
	}
}

func TestSupervisorProposalRejectsInvalidAndZero(t *testing.T) {
	for _, path := range []string{"", "relative", "/a/../b", "/a\x00b", "/a\nb", "/a\x01b", "/a\xffb", "/a\tb", "/a\ufffeb", "/a\uffffb"} {
		if _, err := ProposeSupervisor(path, generation); err == nil {
			t.Fatal(path)
		}
	}
	for _, id := range []string{"", strings.ToUpper(generation), "00000000-0000-0000-0000-000000000000", generation + "<true/>"} {
		if _, err := ProposeSupervisor("/private/operator/supervisor", id); err == nil {
			t.Fatal(id)
		}
	}
	if _, err := (SupervisorJob{}).Plist(); err == nil {
		t.Fatal("zero proposal")
	}
}
