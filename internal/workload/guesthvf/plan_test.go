package guesthvf

import (
	"reflect"
	"strings"
	"testing"
)

var files = Files{"/private/operator/qemu", "/private/operator/firmware.fd", "/private/operator/candidate.qcow2"}

const generation = "b42718a6-5b28-4f82-8d9c-cd91dfe925c1"

func TestFixedPausedDisconnectedInvocation(t *testing.T) {
	p, err := Admit("darwin", "arm64", files, generation)
	if err != nil {
		t.Fatal(err)
	}
	exe, args, env, err := p.Invocation()
	if err != nil || exe != files.Executable || env == nil || len(env) != 0 {
		t.Fatal(exe, env, err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"virt,accel=hvf", "-S", "-nic none", "-serial none", "socket,id=control,fd=3", "format=qcow2,file=" + files.WritableImage} {
		if !strings.Contains(joined, want) {
			t.Fatal("missing", want)
		}
	}
	for _, bad := range []string{"snapshot", "-netdev", "-virtfs", "-fsdev", "accel=tcg", "-daemonize"} {
		if strings.Contains(joined, bad) {
			t.Fatal("unexpected", bad)
		}
	}
	original := append([]string(nil), args...)
	args[0] = "-daemonize"
	_, fresh, _, _ := p.Invocation()
	if !reflect.DeepEqual(original, fresh) {
		t.Fatal("mutable plan")
	}
}
func TestRejectsUnsupportedAndMalformedInputs(t *testing.T) {
	for _, platform := range [][2]string{{"linux", "amd64"}, {"darwin", "amd64"}, {"linux", "arm64"}, {"", ""}} {
		if _, e := Admit(platform[0], platform[1], files, generation); e == nil {
			t.Fatal(platform)
		}
	}
	for _, id := range []string{"", strings.ToUpper(generation), "00000000-0000-0000-0000-000000000000", generation + ",x=y"} {
		if _, e := Admit("darwin", "arm64", files, id); e == nil {
			t.Fatal(id)
		}
	}
	for _, path := range []string{"relative", "/a/../b", "/a,b", "/a\x00b", "/a\nb", "/a\rb", files.Executable} {
		f := files
		f.WritableImage = path
		if _, e := Admit("darwin", "arm64", f, generation); e == nil {
			t.Fatal(path)
		}
	}
	if _, _, _, e := (Plan{}).Invocation(); e == nil {
		t.Fatal("zero plan")
	}
}
