package apirouting

import (
	"reflect"
	"testing"
)

func TestRouteMatchingContracts(t *testing.T) {
	cases := []struct {
		template, path string
		want           map[string]string
		score          int
		ok             bool
	}{
		{"/", "/", nil, 0, true}, {"/users", "/users", nil, 1, true},
		{"/users/{id}", "/users/alice", map[string]string{"id": "alice"}, 1, true},
		{"/users/{id}/tags/{tag}", "/users/alice/tags/red", map[string]string{"id": "alice", "tag": "red"}, 2, true},
		{"/files/{proxy+}", "/files/a/b", map[string]string{"proxy": "a/b"}, 1, true},
		{"/{proxy+}", "/a/../b", map[string]string{"proxy": "a/../b"}, 0, true},
		{"/{proxy+}", "/a//b", map[string]string{"proxy": "a//b"}, 0, true},
		{"/users", "/other", nil, -1, false}, {"/users/next", "/users", nil, -1, false}, {"/users", "/users/next", nil, -1, false},
		{"/{proxy+}", "/", nil, -1, false}, {"/files/{proxy+}", "/files", nil, -1, false},
		{"/{proxy+}/ignored", "/a/b", nil, -1, false}, {"/{+}", "/a", nil, -1, false}, {"/{}", "/a", nil, -1, false},
	}
	for _, c := range cases {
		t.Run(c.template+" -> "+c.path, func(t *testing.T) {
			got, score, ok := MatchPath(c.template, SplitPath(c.path))
			if ok != c.ok || score != c.score || len(got) != len(c.want) || len(got) > 0 && !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v score%d ok%v; want %v score%d ok%v", got, score, ok, c.want, c.score, c.ok)
			}
		})
	}
}
func TestLiteralSpecificityAndPathData(t *testing.T) {
	best, bestScore := "", -1
	for _, template := range []string{"/{proxy+}", "/files/{id}", "/files/static"} {
		_, score, ok := MatchPath(template, SplitPath("/files/static"))
		if ok && score > bestScore {
			best, bestScore = template, score
		}
	}
	if best != "/files/static" {
		t.Fatal(best, bestScore)
	}
	if got := SplitPath("/a//b/"); !reflect.DeepEqual(got, []string{"a", "", "b"}) {
		t.Fatal(got)
	}
	if got := SplitPath("/"); len(got) != 0 {
		t.Fatal(got)
	}
}
